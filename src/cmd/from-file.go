package cmd

/**
 * from-file.go - pull config from file and run
 *
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import (
	"log"
	"os"

	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/info"
	"github.com/notional-labs/gobetween/src/utils"
	"github.com/notional-labs/gobetween/src/utils/codec"
	"github.com/spf13/cobra"
)

/**
 * Add Root Command
 */
func init() {
	RootCmd.AddCommand(FromFileCmd)
}

/**
 * FromFile Command
 */
var FromFileCmd = &cobra.Command{
	Use:   "from-file <path>",
	Short: "Start using config from file",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			cmd.Help() //nolint:errcheck
			return
		}

		data, err := os.ReadFile(args[0])
		if err != nil {
			log.Fatal(err)
		}

		var cfg config.Config

		datastr := string(data)
		if isConfigEnvVars {
			datastr = utils.SubstituteEnvVars(datastr)
		}

		if err = codec.Decode(datastr, &cfg, format); err != nil {
			log.Fatal(err)
		}

		info.Configuration = struct {
			Kind string `json:"kind"`
			Path string `json:"path"`
		}{"file", args[0]}

		start(&cfg)
	},
}
