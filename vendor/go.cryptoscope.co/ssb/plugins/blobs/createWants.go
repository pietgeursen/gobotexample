// SPDX-License-Identifier: MIT

package blobs

import (
	"context"
	"sync"

	"github.com/cryptix/go/logging"
	"github.com/pkg/errors"

	"go.cryptoscope.co/luigi"
	"go.cryptoscope.co/muxrpc"
	"go.cryptoscope.co/ssb"
	"go.cryptoscope.co/ssb/blobstore"
)

type createWantsHandler struct {
	log logging.Interface
	bs  ssb.BlobStore
	wm  ssb.WantManager

	// sources is a map if sources where the responses are read from.
	sources map[string]luigi.Source

	// l protects sources.
	l sync.Mutex
}

// getSource looks if we have a source for that remote and, if not, make a
// source call to get one.
func (h *createWantsHandler) getSource(ctx context.Context, edp muxrpc.Endpoint) (luigi.Source, error) {
	ref := edp.Remote().String()

	h.l.Lock()
	defer h.l.Unlock()

	src, ok := h.sources[ref]
	if ok {
		if src != nil {
			return src, nil
		}
		h.log.Log("msg", "got a nil source from the map, ignoring and making new")
	}

	src, err := edp.Source(ctx, &blobstore.WantMsg{}, muxrpc.Method{"blobs", "createWants"})
	if err != nil {
		return nil, errors.Wrap(err, "error making source call")
	}
	if src == nil {
		h.log.Log("msg", "got a nil source edp.Source, returning an error")
		return nil, errors.New("could not make createWants call")
	}
	h.sources[ref] = src
	return src, nil
}

func (h *createWantsHandler) HandleConnect(ctx context.Context, edp muxrpc.Endpoint) {
	_, err := h.getSource(ctx, edp)
	if err != nil {
		h.log.Log("method", "blobs.createWants", "handler", "onConnect", "getSourceErr", err)
		return
	}
}

func (h *createWantsHandler) HandleCall(ctx context.Context, req *muxrpc.Request, edp muxrpc.Endpoint) {
	src, err := h.getSource(ctx, edp)
	if err != nil {
		h.log.Log("event", "onCall", "handler", "createWants", "getSourceErr", err)
		req.Stream.CloseWithError(errors.Wrap(err, "failed to get source"))
		return
	}
	snk := h.wm.CreateWants(ctx, req.Stream, edp)
	err = luigi.Pump(ctx, snk, src)
	if err != nil && !muxrpc.IsSinkClosed(err) {
		h.log.Log("event", "onCall", "handler", "createWants", "pumpErr", err)
	}

	h.l.Lock()
	defer h.l.Unlock()
	delete(h.sources, edp.Remote().String())

	snk.Close()
	req.Stream.Close()
}
