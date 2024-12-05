package cmd

/**
 * from-url.go - pull config from url and run
 *
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import (
	"io"
	"log"
	"net/http"

	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/info"
	"github.com/notional-labs/gobetween/src/utils"
	"github.com/notional-labs/gobetween/src/utils/codec"
	"github.com/spf13/cobra"
)

/**
 * Add command
 */
func init() {
	RootCmd.AddCommand(FromUrlCmd)
}

/**
 * FromUrlCmd command
 */
var FromUrlCmd = &cobra.Command{
	Use:   "from-url <url>",
	Short: "Start using config from URL",
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) != 1 {
			cmd.Help() //nolint:errcheck
			return
		}

		client := http.Client{}
		res, err := client.Get(args[0])
		if err != nil {
			log.Fatal(err)
		}

		defer res.Body.Close()

		// Read response
		content, err := io.ReadAll(res.Body)
		if err != nil {
			log.Fatal(err)
		}

		datastr := string(content)
		if isConfigEnvVars {
			datastr = utils.SubstituteEnvVars(datastr)
		}

		var cfg config.Config
		if err := codec.Decode(datastr, &cfg, format); err != nil {
			log.Fatal(err)
		}

		info.Configuration = struct {
			Kind string `json:"kind"`
			Url  string `json:"url"`
		}{"url", args[0]}

		start(&cfg)
	},
}
