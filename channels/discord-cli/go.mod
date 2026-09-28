module github.com/trebi-ai/trebi-connectors/channels/discord-cli

go 1.25.5

require (
	github.com/gorilla/websocket v1.5.3
	github.com/urfave/cli/v2 v2.27.7
)

require (
	github.com/cpuguy83/go-md2man/v2 v2.0.7 // indirect
	github.com/russross/blackfriday/v2 v2.1.0 // indirect
	github.com/xrash/smetrics v0.0.0-20240521201337-686a1a2994c1 // indirect
)

require github.com/trebi-ai/trebi-connectors/sdk v0.0.0

replace github.com/trebi-ai/trebi-connectors/sdk => ../../sdk
