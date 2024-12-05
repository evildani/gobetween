package healthcheck

/**
 * ping.go - TCP ping healthcheck
 *
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import (
	"net"
	"time"

	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/core"
	"github.com/notional-labs/gobetween/src/logging"
)

/**
 * Ping healthcheck
 */
func ping(t core.Target, cfg config.HealthcheckConfig, result chan<- CheckResult) {
	pingTimeoutDuration, _ := time.ParseDuration(cfg.Timeout)

	log := logging.For("healthcheck/ping")

	checkResult := CheckResult{
		Target: t,
	}

	conn, err := net.DialTimeout("tcp", t.Address(), pingTimeoutDuration)
	if err != nil {
		checkResult.Status = Unhealthy
	} else {
		checkResult.Status = Healthy
		conn.Close()
	}

	select {
	case result <- checkResult:
	default:
		log.Warn("Channel is full. Discarding value")
	}
}
