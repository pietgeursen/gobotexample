# sunrise-social-go-sbot

this repo helps you build [go-ssb](https://github.com/cryptoscope/ssb) into an android / iOS (untested) library. 

# gomobile

The [Mobile page on the golang wiki](https://github.com/golang/go/wiki/Mobile) is a good starting intro. Use the SDK instructions but change `golang.org/x/mobile/example/basic` with this package. Probably want to fork it first and then add all your stuff like passing configs from android and setting up storage locations and what not... If you fork it, replace `github.com/cryptix/gobotexample` with your own import path!

## gomobile modules workaround

there is some weirdness with Go Modules and gomobile, you want to use `export GO111MODULE=off` for the foresable future to run the `gomobile bind ...` call.

The current checked in deps should work but here is how to update them:

```bash
# switch on module mode to update the `go.mod` file
export GO111MODULE=on

# pull in your wanted version of a dep
go get go.cryptoscope.co/ssb@someNewStuffOrVersion

# copy the dependencies into the `vendor/` folder
go mod vendor

# switch of module module mode for the actual building
export GO111MODULE=off

# compile all the dependencies
# the output should include lines containing github.com/cryptix/gobotexample/vendor/.... (or what ever you changed the impport of this project to)
go build -v -i

# run the (very basic) tests to see the bot starts and stops
go test

# build the cross-compiled bindings
gomobile bind -o app/gosbot.aar -target=android github.com/cryptix/gobotexample
```

As a sanity check there is a simple start>sleep(1)>stop test (run `go test`) that you can use before running `gomobile ..` to see if the code compiles.
