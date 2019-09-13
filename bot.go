//go:generate go-mobile-collection $GOFILE

package gobotexample

import (
	"context"
	"encoding/base64"
	"os"
	"time"
  "bytes"

	"github.com/cryptix/go/logging"

	mksbot "go.cryptoscope.co/ssb/sbot"
	"go.cryptoscope.co/ssb/multilogs"
	"go.cryptoscope.co/ssb"
)

var (
	// helper
	log        logging.Interface
	checkFatal = logging.CheckFatal

	// juicy bits
	appKey  string = "1KHLiKZvAvjbY1ziZEHMXawbCEIM6qwjCDm3VYRan/s="
	hmacSec string

	theBot *mksbot.Sbot
)

func checkAndLog(err error) {
	if err != nil {
		if err := logging.LogPanicWithStack(log, "checkAndLog", err); err != nil {
			panic(err)
		}
	}
}

// @collection-wrapper
type Recipients struct {
 RecipientKey string
}
func (*Recipients) Equal(rhs *Recipients) bool{
	return true
}

//How much is checked by the stack if a not json string gets passed?
func Publish(message string, recipientKeys []byte) error {
  var recps = RecipientsCollection{}
  recps.UnmarshalJSON(recipientKeys)

	publish, err := multilogs.OpenPublishLog(theBot.RootLog, theBot.UserFeeds, *theBot.KeyPair)
  _, err = publish.Append(message)
  return err
}

func WhoAmI()string{
  return ""
}
func NetworkOn(){}
func NetworkOff(){}
func NetworkIsOn() bool {
  return false
}

func AdvertisingOn() error {
  return nil
}
func AdvertisingOff() error {
  return nil
}
func AdvertisingIsOn() bool {
  return false
}
func DiscoveryOn() error {
  return nil
}
func DiscoveryOff() error {
  return nil
}
func DiscoveryIsOn() bool {
  return false
}

func GossipAdd(multiserveraddress string) error {
  return nil
}
func GossipStop(multiserveraddress string) error {
  return nil
}
func GossipConnect(multiserveraddress string) error {
  return nil
}

func BlobsWant(){}
func BlobsRemove(){}
func BlobsList(){}
func BlobsHas(){}
func BlobsAdd(blob []byte) (string, error){
  r := bytes.NewReader(blob)
  ref, err := theBot.BlobStore.Put(r)
	if err != nil {
		return "", err
	}
  return ref.Ref(), nil
}

func BlobsGet(refStr string) ([]byte, error){
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

func Stop() error {
	theBot.Shutdown()
	return theBot.Close()
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
	)
	checkFatal(err)

	id := theBot.KeyPair.Id

	checkFatal(err)
	log.Log("event", "serving", "ID", id.Ref(), "addr", listenAddr)
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
