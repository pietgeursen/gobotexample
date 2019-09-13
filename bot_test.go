package gobotexample_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.cryptoscope.co/margaret"

	"github.com/pietgeursen/gobotexample"
	"github.com/stretchr/testify/require"
)

func TestStartStop(t *testing.T) {
	r := require.New(t)

	tRepoDir := filepath.Join("testrun", t.Name())
	os.RemoveAll(tRepoDir)
	err := os.MkdirAll(tRepoDir, 0700)
	r.NoError(err)

	// panics if there is a problem, could return an error instead
	gobotexample.Start(tRepoDir)

	// time.Sleep(1 * time.Second)

	r.NoError(gobotexample.Stop(), "failed to stop after 1st start")
}

func TestPublish(t *testing.T) {
	r := require.New(t)

	tRepoDir := filepath.Join("testrun", t.Name())
	os.RemoveAll(tRepoDir)
	err := os.MkdirAll(tRepoDir, 0700)
	r.NoError(err)

	// panics if there is a problem, could return an error instead
	gobotexample.Start(tRepoDir)

	c, err := gobotexample.CurrentMessageCount()
	r.NoError(err)
	r.EqualValues(margaret.SeqEmpty, c)

	err = gobotexample.Publish(`{"type":"test", "hello":"piet"}`, nil)
	r.NoError(err)

	c, err = gobotexample.CurrentMessageCount()
	r.NoError(err)
	r.EqualValues(0, c)

	data, err := gobotexample.PullMessages(0, 100)
	r.NoError(err)
	r.EqualValues(0, c)

	var v []interface{}
	err = json.Unmarshal(data, &v)
	r.NoError(err)
	r.EqualValues(len(v), 1)

	err = gobotexample.Publish(`{"type":"another", "hello":"piet!!!"}`, nil)
	r.NoError(err)

	c, err = gobotexample.CurrentMessageCount()
	r.NoError(err)
	r.EqualValues(1, c)
	t.Log(v)

	data, err = gobotexample.PullMessages(0, 100)
	r.NoError(err)

	var typedVs []gobotexample.MessageWithRootSeq
	err = json.Unmarshal(data, &typedVs)
	r.NoError(err)
	r.EqualValues(len(typedVs), 2)
	r.EqualValues(typedVs[0].RootSeq, 0)
	r.EqualValues(typedVs[1].RootSeq, 1)

	r.NoError(gobotexample.Stop(), "failed to stop after 1st start")
}
