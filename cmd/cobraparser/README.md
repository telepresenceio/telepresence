# Cobra Help Output Parser

## Parser
The `cobraparser` is a tool to parse the output produced by the `spf13.CobraCommand` help function. The parser invokes an executable with `--help` and creates JSON structured data from the resulting output. Subcommands are traversed recursively.

## Generator Package
The `generator` package contains the code to dynamically configure a `spf13.CobraCommand` using the JSON data produced by the parser. The command will be populated with the subcommands and flags defined in the JSON data.

## Sample usage:

See `build-aux/main.mk` (the `pkg/client/cli/docker/compose/dc-cli.json` target) and the corresponding use of the generator package in `pkg/client/cli/docker/compose/config.go`.
