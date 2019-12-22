//go:generate go-mobile-collection $GOFILE

package gobotexample

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/cryptix/go/logging"
	"github.com/go-kit/kit/log/level"
	"github.com/gorilla/mux"
	"github.com/pkg/errors"
	"go.cryptoscope.co/luigi"
	"go.cryptoscope.co/margaret"
	"go.cryptoscope.co/margaret/multilog"
	"go.cryptoscope.co/netwrap"
	"go.cryptoscope.co/secretstream"

	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/blobstore"
	"go.cryptoscope.co/ssb/multilogs"
	"go.cryptoscope.co/ssb/repo"
	mksbot "go.cryptoscope.co/ssb/sbot"

	"github.com/sunrise-choir/sunrise-social-gobot/internal/multiserver"
)

var (
	// helper
	log        logging.Interface
	checkFatal = logging.CheckFatal

	// juicy bits
	appKey  string = "1KHLiKZvAvjbY1ziZEHMXawbCEIM6qwjCDm3VYRan/s="
	hmacSec string

	runningLock sync.Mutex
	theBot      *mksbot.Sbot
)

func checkAndLog(err error) {
	if err != nil {
		panic(err)
	}
}

var ErrNotInitialized = errors.New("gobot: not initialized")

//How much is checked by the stack if a not json string gets passed?
func Publish(message string) error {

	var v interface{}
	err := json.Unmarshal([]byte(message), &v)
	if err != nil {
		return errors.Wrap(err, "publish: invalid json input")
	}

	_, err = theBot.PublishLog.Publish(v)
	return err
}

func CurrentMessageCount() (int64, error) {
	runningLock.Lock()
	if theBot == nil {
		runningLock.Unlock()
		return -2, ErrNotInitialized
	}
	runningLock.Unlock()

	v, err := theBot.RootLog.Seq().Value()
	if err != nil {
		return -2, errors.Wrap(err, "query creation failed")
	}

	seq := v.(margaret.Seq)

	return seq.Seq(), nil
}

type MessageWithRootSeq struct {
	ssb.KeyValueRaw
	RootSeq int64 `json:"seqField"`
}

func PullMessages(last, limit int) ([]byte, error) {
	runningLock.Lock()
	if theBot == nil {
		runningLock.Unlock()
		return nil, ErrNotInitialized
	}
	runningLock.Unlock()

	src, err := theBot.RootLog.Query(
		margaret.Gte(margaret.BaseSeq(last)),
		margaret.Limit(limit),
		margaret.SeqWrap(true))
	if err != nil {
		return nil, errors.Wrap(err, "query creation failed")
	}

	w := &bytes.Buffer{}
	i := 0
	fmt.Fprint(w, "[")
	for {
		v, err := src.Next(context.Background())
		if err != nil {
			if luigi.IsEOS(err) {
				fmt.Fprint(w, "]")
				break
			}
			return nil, errors.Wrapf(err, "drainLog: failed to drain log msg:%d", i)
		}
		if i > 0 {
			fmt.Fprint(w, ",")
		}

		sw, ok := v.(margaret.SeqWrapper)
		if !ok {
			return nil, errors.Errorf("publish: unexpected message type: %T", v)
		}

		storedMsg, ok := sw.Value().(ssb.Message)
		if !ok {
			return nil, errors.Errorf("publish: unexpected message type: %T", v)
		}

		msgValue := storedMsg.ValueContent()
		var msg MessageWithRootSeq
		msg.RootSeq = sw.Seq().Seq()
		msg.Key_ = storedMsg.Key()
		msg.Value = *msgValue

		if err := json.NewEncoder(w).Encode(msg); err != nil {
			return nil, errors.Wrapf(err, "drainLog: failed to k:v map message %d", i)
		}

		i++
	}
	return w.Bytes(), nil
}

func WhoAmI() string {
	return theBot.KeyPair.Id.Ref()
}

func GossipConnect(multiserveraddress string) error {
	runningLock.Lock()
	if theBot == nil {
		runningLock.Unlock()
		return ErrNotInitialized
	}
	runningLock.Unlock()

	msaddr, err := multiserver.ParseNetAddress([]byte(multiserveraddress))
	if err != nil {
		return errors.Wrapf(err, "gossip.connect call: failed to parse input")
	}

	wrappedAddr := netwrap.WrapAddr(&msaddr.Addr, secretstream.Addr{PubKey: msaddr.Ref.ID})
	fmt.Println("doing gossip.connect", wrappedAddr.String())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(15 * time.Minute)
		cancel()
	}()
	err = theBot.Network.Connect(ctx, wrappedAddr)
	return errors.Wrapf(err, "gossip.connect call: error connecting to %q", msaddr.Addr)
}

func BlobsWant(blobref string) {
	br, err := ssb.ParseBlobRef(blobref)
	if err != nil {
		log.Log("blobsWant", "failed to parse reference", "ref", blobref, "err", err)
		return
	}

	err = theBot.WantManager.Want(br)
	if err != nil {
		log.Log("blobsWant", "failed add want to local peer", "ref", blobref, "err", err)
		return
	}
}

func BlobsRemove() {}
func BlobsList()   {}
func BlobsHas()    {}
func BlobsAdd(blob []byte) (string, error) {
	r := bytes.NewReader(blob)
	ref, err := theBot.BlobStore.Put(r)
	if err != nil {
		return "", err
	}
	return ref.Ref(), nil
}

func BlobsGet(refStr string) ([]byte, error) {
	ref, err := ssb.ParseBlobRef(refStr)
	if err != nil {
		return nil, err
	}

	r, err := theBot.BlobStore.Get(ref)
	if err != nil {
		return nil, err
	}

	buf := new(bytes.Buffer)
	_, err = buf.ReadFrom(r)
	if err != nil {
		return nil, err
	}
	var slice = buf.Bytes()
	return slice, nil
}

func Peers() ([]byte, error) {
	w := &bytes.Buffer{}
	status, err := theBot.Status()
	if err != nil {
		return nil, err
	}
	var peers = status.Peers

	// Apparently peers can be null and that's painful for converting to json and back
	// If peers is null then set it to an empty list.
	if peers == nil {
		peers = []ssb.PeerStatus{}
	}
	if err := json.NewEncoder(w).Encode(peers); err != nil {
		return nil, err
	}

	return w.Bytes(), nil
}

func Stop() error {
	theBot.Shutdown()
	return theBot.Close()
}

// use old badger implementation for now
func openBadgerUserFeeds(r repo.Interface) (multilog.MultiLog, repo.ServeFunc, error) {
	return repo.OpenBadgerMultiLog(r, "userFeeds", multilogs.UserFeedsUpdate)
}

func Start(repoPath string) {

	logging.SetupLogging(os.Stderr)
	log = logging.Logger("sbot")

	ctx := context.Background()

	ak, err := base64.StdEncoding.DecodeString(appKey)
	checkFatal(err)

	listenAddr := ":8008"
	theBot, err = mksbot.New(
		mksbot.WithInfo(log),
		mksbot.WithAppKey(ak),
		mksbot.WithRepoPath(repoPath),
		mksbot.WithListenAddr(listenAddr),
		mksbot.EnableAdvertismentBroadcasts(true),
		mksbot.EnableAdvertismentDialing(true),
		//mksbot.LateOption(mksbot.MountMultiLog("userFeeds", openBadgerUserFeeds)),
	)
	checkFatal(err)

	id := theBot.KeyPair.Id

	checkFatal(err)
	log.Log("event", "serving", "ID", id.Ref(), "addr", listenAddr)

	// open listener first (so we can error out if the port is taken)
	// TODO: could maybe be localhost?
	lis, err := net.Listen("tcp", "0.0.0.0:8091")
	checkFatal(errors.Wrap(err, "blobsServ listen failed"))

	go func() {
		r := mux.NewRouter()

		r.HandleFunc("/blobs/{blobHash}", func(w http.ResponseWriter, r *http.Request) {
			vars := mux.Vars(r)

			blobHash := vars["blobHash"]
			blobRef, err := ssb.ParseBlobRef(blobHash)
			if err != nil {
				http.Error(w, "bad request", http.StatusBadRequest)
				level.Error(log).Log("msg", "failed to parse url parameter", "err", err)
				return
			}

			blobReader, err := theBot.BlobStore.Get(blobRef)
			if err != nil {
				if errors.Cause(err) == blobstore.ErrNoSuchBlob {
					theBot.WantManager.Want(blobRef)

					received := make(chan struct{})
					cancel := theBot.BlobStore.Changes().Register(luigi.FuncSink(func(ctx context.Context, v interface{}, err error) error {
						if err != nil {
							if luigi.IsEOS(err) {
								return nil
							}
							return err
						}

						n, ok := v.(ssb.BlobStoreNotification)
						if !ok {
							return errors.Errorf("blob change: unhandled notification type: %T", v)
						}

						if n.Op != ssb.BlobStoreOpPut {
							return nil
						}

						if !n.Ref.Equal(blobRef) {
							return nil // yet another blob, ignore
						}
						close(received)
						return nil
					}))
					defer cancel()

					// wait for timeout or the changes register to close the channel
					select {
					case <-time.After(30 * time.Second):
						http.Error(w, "blob wait timeout", http.StatusNotFound)
						return
					case <-received:
						blobReader, err = theBot.BlobStore.Get(blobRef)
						if err != nil {
							http.Error(w, "internal error", http.StatusInternalServerError)
							level.Error(log).Log("msg", "get failure after received", "err", err)
							return
						}
					}

				} else {
					http.Error(w, err.Error(), http.StatusInternalServerError)
					level.Error(log).Log("err", err)
					return
				}
			}

			io.Copy(w, blobReader)
		})

		http.Serve(lis, r)
	}()

	go func() {
		for {
			// Note: This is where the serving starts ;)
			err = theBot.Network.Serve(ctx)
			log.Log("event", "sbot node.Serve returned, restarting..", "err", err)
			time.Sleep(1 * time.Second)
			select {
			case <-ctx.Done():
				os.Exit(0)
			default:
			}
		}
	}()
}
