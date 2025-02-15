// SPDX-License-Identifier: MIT

package network

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/agl/ed25519"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/go-kit/kit/metrics"
	"github.com/pkg/errors"
	"go.cryptoscope.co/muxrpc"
	"go.cryptoscope.co/netwrap"
	"go.cryptoscope.co/secretstream"
	"go.cryptoscope.co/secretstream/secrethandshake"

	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/internal/ctxutils"
)

// DefaultPort is the default listening port for ScuttleButt.
const DefaultPort = 8008

type Options struct {
	Logger log.Logger

	Dialer     netwrap.Dialer
	ListenAddr net.Addr

	AdvertsSend      bool
	AdvertsConnectTo bool

	KeyPair     *ssb.KeyPair
	AppKey      []byte
	MakeHandler func(net.Conn) (muxrpc.Handler, error)

	ConnTracker ssb.ConnTracker

	// PreSecureWrappers are applied before the shs+boxstream wrapping takes place
	// usefull for accessing the sycall.Conn to apply control options on the socket
	BefreCryptoWrappers []netwrap.ConnWrapper

	// AfterSecureWrappers are applied afterwards, usefull to debug muxrpc content
	AfterSecureWrappers []netwrap.ConnWrapper

	EventCounter    metrics.Counter
	SystemGauge     metrics.Gauge
	Latency         metrics.Histogram
	EndpointWrapper func(muxrpc.Endpoint) muxrpc.Endpoint
}

type node struct {
	opts Options

	log log.Logger

	closed   bool
	closedMu sync.RWMutex

	dialer        netwrap.Dialer
	l             net.Listener
	localDiscovRx *Discoverer
	localDiscovTx *Advertiser
	secretServer  *secretstream.Server
	secretClient  *secretstream.Client
	connTracker   ssb.ConnTracker

	beforeCryptoConnWrappers []netwrap.ConnWrapper
	afterSecureConnWrappers  []netwrap.ConnWrapper

	remotesLock sync.Mutex
	remotes     map[string]muxrpc.Endpoint

	edpWrapper func(muxrpc.Endpoint) muxrpc.Endpoint
	evtCtr     metrics.Counter
	sysGauge   metrics.Gauge
	latency    metrics.Histogram
}

func New(opts Options) (ssb.Network, error) {
	n := &node{
		opts:    opts,
		remotes: make(map[string]muxrpc.Endpoint),
	}

	if opts.ConnTracker == nil {
		opts.ConnTracker = NewAcceptAllTracker()
	}
	n.connTracker = opts.ConnTracker

	var err error

	if opts.Dialer != nil {
		n.dialer = opts.Dialer
	} else {
		n.dialer = netwrap.Dial
	}

	n.secretClient, err = secretstream.NewClient(opts.KeyPair.Pair, opts.AppKey)
	if err != nil {
		return nil, errors.Wrap(err, "error creating secretstream.Client")
	}

	n.secretServer, err = secretstream.NewServer(opts.KeyPair.Pair, opts.AppKey)
	if err != nil {
		return nil, errors.Wrap(err, "error creating secretstream.Server")
	}

	// TODO: make multiple listeners (localhost:8008 should not restrict or kill connections)
	lisWrap := netwrap.NewListenerWrapper(n.secretServer.Addr(), append(opts.BefreCryptoWrappers, n.secretServer.ConnWrapper())...)
	n.l, err = netwrap.Listen(n.opts.ListenAddr, lisWrap)
	if err != nil {
		return nil, errors.Wrap(err, "error creating listener")
	}

	if n.opts.AdvertsSend {
		n.localDiscovTx, err = NewAdvertiser(n.opts.ListenAddr, opts.KeyPair)
		if err != nil {
			return nil, errors.Wrap(err, "error creating Advertiser")
		}
	}

	if n.opts.AdvertsConnectTo {
		n.localDiscovRx, err = NewDiscoverer(opts.KeyPair)
		if err != nil {
			return nil, errors.Wrap(err, "error creating Advertiser")
		}
	}

	n.beforeCryptoConnWrappers = opts.BefreCryptoWrappers
	n.afterSecureConnWrappers = opts.AfterSecureWrappers

	n.edpWrapper = opts.EndpointWrapper
	n.evtCtr = opts.EventCounter
	n.sysGauge = opts.SystemGauge
	n.latency = opts.Latency

	if n.sysGauge != nil {
		n.sysGauge.With("part", "conns").Set(0)
		n.sysGauge.With("part", "fetches").Set(0)

		n.connTracker = NewInstrumentedConnTracker(n.connTracker, n.sysGauge, n.latency)
	}
	n.log = opts.Logger

	return n, nil
}

func (n *node) GetConnTracker() ssb.ConnTracker {
	return n.connTracker
}

// GetEndpointFor returns a muxrpc endpoint to call the remote identified by the passed feed ref
// retruns false if there is no such connection
// TODO: merge with conntracker
func (n *node) GetEndpointFor(ref *ssb.FeedRef) (muxrpc.Endpoint, bool) {
	if ref == nil {
		return nil, false
	}
	n.remotesLock.Lock()
	defer n.remotesLock.Unlock()

	edp, has := n.remotes[ref.Ref()]
	return edp, has
}

// TODO: merge with conntracker
func (n *node) GetAllEndpoints() []ssb.EndpointStat {
	n.remotesLock.Lock()
	defer n.remotesLock.Unlock()

	var stats []ssb.EndpointStat

	for ref, edp := range n.remotes {
		id, _ := ssb.ParseFeedRef(ref)
		remote := edp.Remote()
		ok, durr := n.connTracker.Active(remote)
		if !ok {
			continue
		}
		stats = append(stats, ssb.EndpointStat{
			ID:       id,
			Addr:     remote,
			Since:    durr,
			Endpoint: edp,
		})
	}
	return stats
}

// TODO: merge with conntracker
func (n *node) addRemote(edp muxrpc.Endpoint) {
	n.remotesLock.Lock()
	defer n.remotesLock.Unlock()
	r, err := ssb.GetFeedRefFromAddr(edp.Remote())
	if err != nil {
		panic(err)
	}
	// ref := r.Ref()
	// if oldEdp, has := n.remotes[ref]; has {
	// n.log.Log("remotes", "previous active", "ref", ref)
	// c := client.FromEndpoint(oldEdp)
	// _, err := c.Whoami()
	// if err == nil {
	// 	// old one still works
	// 	return
	// }
	// }
	// replace with new
	n.remotes[r.Ref()] = edp
}

// TODO: merge with conntracker
func (n *node) removeRemote(edp muxrpc.Endpoint) {
	n.remotesLock.Lock()
	defer n.remotesLock.Unlock()
	r, err := ssb.GetFeedRefFromAddr(edp.Remote())
	if err != nil {
		panic(err)
	}
	delete(n.remotes, r.Ref())
}

func (n *node) handleConnection(ctx context.Context, origConn net.Conn, hws ...muxrpc.HandlerWrapper) {
	// TODO: overhaul events and logging levels
	n.closedMu.RLock()
	defer n.closedMu.RUnlock()
	if n.closed {
		origConn.Close()
		level.Warn(n.log).Log("conn", "ignored", "msg", "netwrok closed")
		return
	}

	conn, err := n.applyConnWrappers(origConn)
	if err != nil {
		origConn.Close()
		n.log.Log("msg", "node/Serve: failed to wrap connection", "err", err)
		return
	}

	ok := n.connTracker.OnAccept(conn)
	if !ok {
		err := origConn.Close()
		n.log.Log("conn", "ignored", "remote", conn.RemoteAddr(), "err", err)
		return
	}
	var edp muxrpc.Endpoint

	ctx, cancel := ctxutils.WithError(ctx, fmt.Errorf("handle conn returned"))

	defer func() {
		durr := n.connTracker.OnClose(conn)
		var edpTerm, connClose error
		if edp != nil {
			edpTerm = errors.Wrap(edp.Terminate(), "packer closing")
		}
		connClose = errors.Wrap(origConn.Close(), "direct conn closing")
		level.Debug(n.log).Log("event", "conn-closing", "edpTerm", edpTerm, "connClose", connClose, "durr", fmt.Sprintf("%v", durr))
		cancel()
	}()

	if n.evtCtr != nil {
		n.evtCtr.With("event", "connection").Add(1)
	}

	h, err := n.opts.MakeHandler(conn)
	if err != nil {
		// n.log.Log("conn", "mkHandler", "err", err, "peer", conn.RemoteAddr())
		if _, ok := errors.Cause(err).(*ssb.ErrOutOfReach); ok {
			return // ignore silently
		}
		return
	}

	for _, hw := range hws {
		h = hw(h)
	}

	pkr := muxrpc.NewPacker(conn)
	filtered := level.NewFilter(n.log, level.AllowInfo())
	edp = muxrpc.HandleWithLogger(pkr, h, filtered)

	if n.edpWrapper != nil {
		edp = n.edpWrapper(edp)
	}
	n.addRemote(edp)

	srv := edp.(muxrpc.Server)

	if err := srv.Serve(ctx); err != nil {
		level.Debug(n.log).Log("conn", "serve", "err", err, "peer", conn.RemoteAddr())
	}
	n.removeRemote(edp)
}

func (n *node) Serve(ctx context.Context, wrappers ...muxrpc.HandlerWrapper) error {
	if n.localDiscovTx != nil {
		// should also be called when ctx canceled?
		// or pass ctx to adv start?
		n.localDiscovTx.Start()

		defer n.localDiscovTx.Stop()
	}

	if n.localDiscovRx != nil {
		ch, done := n.localDiscovRx.Notify()
		defer done() // might trigger close of closed panic
		go func() {
			for a := range ch {
				if is, _ := n.connTracker.Active(a); is {
					//n.log.Log("event", "debug", "msg", "ignoring active", "addr", a.String())
					continue
				}
				err := n.Connect(ctx, a)
				if err == nil {
					continue
				}
				if _, ok := errors.Cause(err).(secrethandshake.ErrProtocol); !ok {
					level.Debug(n.log).Log("event", "discovery dialback", "err", err, "addr", a.String())
				}

			}
		}()
	}

	defer level.Debug(n.log).Log("event", "network listen loop exited")
	for {
		n.closedMu.RLock()
		isClosed := n.closed
		n.closedMu.RUnlock()
		if isClosed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		conn, err := n.l.Accept()
		if err != nil {
			if strings.Contains(err.Error(), "use of closed network connection") {
				// yikes way of handling this
				// but means this needs to be restarted anyway
				return nil
			}

			switch cause := errors.Cause(err).(type) {

			case secrethandshake.ErrProtocol:
				// ignore
			default:
				if cause != io.EOF { // handshake ended early
					n.log.Log("msg", "node/Serve: failed to accept connection", "err", err,
						"cause", cause, "causeT", fmt.Sprintf("%T", cause))
				}
			}
			continue
		}

		go func(c net.Conn) {
			n.handleConnection(ctx, c, wrappers...)
		}(conn)
	}
}

func (n *node) Connect(ctx context.Context, addr net.Addr) error {
	n.closedMu.RLock()
	defer n.closedMu.RUnlock()
	if n.closed {
		return errors.New("network: node closed")
	}

	shsAddr := netwrap.GetAddr(addr, "shs-bs")
	if shsAddr == nil {
		return errors.New("node/connect: expected an address containing an shs-bs addr")
	}

	var pubKey [ed25519.PublicKeySize]byte
	if shsAddr, ok := shsAddr.(secretstream.Addr); ok {
		copy(pubKey[:], shsAddr.PubKey)
	} else {
		return errors.New("node/connect: expected shs-bs address to be of type secretstream.Addr")
	}

	conn, err := n.dialer(netwrap.GetAddr(addr, "tcp"), append(n.beforeCryptoConnWrappers, n.secretClient.ConnWrapper(pubKey))...)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		return errors.Wrap(err, "node/connect: error dialing")
	}

	go func(c net.Conn) {
		n.handleConnection(ctx, c)
	}(conn)
	return nil
}

func (n *node) GetListenAddr() net.Addr {
	return n.l.Addr()
}

func (n *node) applyConnWrappers(conn net.Conn) (net.Conn, error) {
	for i, cw := range n.afterSecureConnWrappers {
		var err error
		conn, err = cw(conn)
		if err != nil {
			return nil, errors.Wrapf(err, "error applying connection wrapper #%d", i)
		}
	}
	return conn, nil
}

func (n *node) Closed() bool {
	n.closedMu.RLock()
	isClosed := n.closed
	n.closedMu.RUnlock()
	return isClosed
}

func (n *node) Close() error {
	n.closedMu.Lock()
	defer n.closedMu.Unlock()
	if n.localDiscovTx != nil {
		n.localDiscovTx.Stop()
	}

	err := n.l.Close()
	if err != nil {
		return errors.Wrap(err, "ssb: network node failed to close it's listener")
	}

	n.remotesLock.Lock()
	defer n.remotesLock.Unlock()
	for addr, edp := range n.remotes {
		if err := edp.Terminate(); err != nil {
			n.log.Log("event", "failed to terminate endpoint", "addr", addr, "err", err)
		}
	}

	if cnt := n.connTracker.Count(); cnt > 0 {
		n.log.Log("event", "warning", "msg", "still open connections", "count", cnt)
		n.connTracker.CloseAll()
	}
	n.closed = true

	return nil
}
