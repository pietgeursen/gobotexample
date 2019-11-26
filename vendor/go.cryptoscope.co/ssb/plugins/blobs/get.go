// SPDX-License-Identifier: MIT

package blobs

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"

	"github.com/cryptix/go/logging"
	"github.com/pkg/errors"

	"go.cryptoscope.co/muxrpc"

	"go.cryptoscope.co/ssb"
)

type getHandler struct {
	bs  ssb.BlobStore
	log logging.Interface
}

func (getHandler) HandleConnect(context.Context, muxrpc.Endpoint) {}

func (h getHandler) HandleCall(ctx context.Context, req *muxrpc.Request, edp muxrpc.Endpoint) {
	logger := log.With(h.log, "handler", "get")
	errLog := level.Error(logger)
	// dbg := level.Debug(logger)

	// dbg.Log("event", "onCall", "method", req.Method)
	// defer dbg.Log("event", "onCall", "handler", "get-return", "method", req.Method)

	// TODO: push manifest check into muxrpc
	if req.Type == "" {
		req.Type = "source"
	}

	args := req.Args()
	if len(args) != 1 {
		req.Stream.CloseWithError(fmt.Errorf("bad request - wrong args (%d)", len(args)))
		return
	}

	var refStr string
	switch arg := args[0].(type) {
	case string:
		refStr = arg
	case map[string]interface{}:
		refStr, _ = arg["key"].(string)
	}

	ref, err := ssb.ParseBlobRef(refStr)
	if err != nil {
		err = errors.Wrap(err, "error parsing blob reference")
		req.Stream.CloseWithError(err)
		checkAndLog(errLog, err)
		return
	}

	logger = log.With(logger, "blob", ref.Ref())
	info := level.Info(logger)
	errLog = level.Error(logger)

	r, err := h.bs.Get(ref)
	if err != nil {
		err = req.Stream.CloseWithError(errors.New("do not have blob"))
		checkAndLog(errLog, errors.Wrap(err, "error closing stream with error"))
		return
	}
	start := time.Now()

	w := muxrpc.NewSinkWriter(req.Stream)
	_, err = io.Copy(w, r)
	checkAndLog(errLog, errors.Wrap(err, "error sending blob"))

	err = w.Close()
	checkAndLog(errLog, errors.Wrap(err, "error closing blob output"))
	if err == nil {
		info.Log("event", "transmission successfull", "took", time.Since(start))
	}
}
