package server

/**
 * server.go - server creator
 *
 * @author Illarion Kovalchuk
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import (
	"errors"

	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/core"
	"github.com/notional-labs/gobetween/src/server/tcp"
	"github.com/notional-labs/gobetween/src/server/udp"
)

/**
 * Creates new Server based on cfg.Protocol
 */
func New(name string, cfg config.Server) (core.Server, error) {
	switch cfg.Protocol {
	case "tls", "tcp":
		return tcp.New(name, cfg)
	case "vxlan", "udp", "geneve":
		return udp.New(name, cfg)
	default:
		return nil, errors.New("Can't create server for protocol " + cfg.Protocol)
	}
}
