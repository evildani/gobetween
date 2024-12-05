package discovery

/**
 * discovery.go - discovery
 *
 * @author Yaroslav Pogrebnyak <yyyaroslav@gmail.com>
 */

import (
	"time"

	"github.com/notional-labs/gobetween/src/config"
	"github.com/notional-labs/gobetween/src/core"
	"github.com/notional-labs/gobetween/src/logging"
)

/**
 * Registry of factory methods for Discoveries
 */
var registry = make(map[string]func(config.DiscoveryConfig) interface{})

/**
 * Initialize type registry
 */
func init() {
	registry["static"] = NewStaticDiscovery
	registry["srv"] = NewSrvDiscovery
	registry["docker"] = NewDockerDiscovery
	registry["json"] = NewJsonDiscovery
	registry["exec"] = NewExecDiscovery
	registry["plaintext"] = NewPlaintextDiscovery
	registry["consul"] = NewConsulDiscovery
	registry["lxd"] = NewLXDDiscovery
}

/**
 * Create new Discovery based on strategy
 */
func New(strategy string, cfg config.DiscoveryConfig) *Discovery {
	return registry[strategy](cfg).(*Discovery)
}

/**
 * Fetch func for pullig backends
 */
type FetchFunc func(config.DiscoveryConfig) (*[]core.Backend, error)

/**
 * Options for pull discovery
 */
type DiscoveryOpts struct {
	RetryWaitDuration time.Duration
}

/**
 * Discovery
 */
type Discovery struct {
	/**
	 * Cached backends
	 */
	backends *[]core.Backend

	/**
	 * Function to fetch / discovery backends
	 */
	fetch FetchFunc

	/**
	 * Options for fetch
	 */
	opts DiscoveryOpts

	/**
	 * Discovery configuration
	 */
	cfg config.DiscoveryConfig

	/**
	 * Channel where to push newly discovered backends
	 */
	out chan ([]core.Backend)

	/**
	 * Channel for stopping discovery
	 */
	stop chan bool
}

/**
 * Pull / fetch backends loop
 */
func (discovery *Discovery) Start() {
	log := logging.For("discovery")

	discovery.out = make(chan []core.Backend)
	discovery.stop = make(chan bool)

	// Prepare interval
	interval, err := time.ParseDuration(discovery.cfg.Interval)
	if err != nil {
		log.Fatal(err)
	}

	// TODO: rewrite with channels for stop
	go func() {
		for {
			backends, err := discovery.fetch(discovery.cfg)

			select {
			case <-discovery.stop:
				log.Info("Stopping discovery ", discovery.cfg)
				return
			default:
			}

			if err != nil {
				log.Error(discovery.cfg.Kind, " error ", err, " retrying in ", discovery.opts.RetryWaitDuration.String())
				log.Info("Applying failpolicy ", discovery.cfg.Failpolicy)

				if discovery.cfg.Failpolicy == "setempty" {
					discovery.backends = &[]core.Backend{}
					if !discovery.send() {
						log.Info("Stopping discovery ", discovery.cfg)
						return
					}
				}

				if !discovery.wait(discovery.opts.RetryWaitDuration) {
					log.Info("Stopping discovery ", discovery.cfg)
					return
				}

				continue
			}

			// cache
			discovery.backends = backends
			if !discovery.send() {
				log.Info("Stopping discovery ", discovery.cfg)
				return
			}

			// exit gorouting if no cacheTtl
			// used for static discovery
			if interval == 0 {
				return
			}

			if !discovery.wait(interval) {
				log.Info("Stopping discovery ", discovery.cfg)
				return
			}
		}
	}()
}

func (discovery *Discovery) send() bool {
	// out if not stopped
	select {
	case <-discovery.stop:
		return false
	default:
		discovery.out <- *discovery.backends
		return true
	}
}

/**
 * wait waits for interval or stop
 * returns true if waiting was successfull
 * return false if waiting was interrupted with stop
 */
func (discovery *Discovery) wait(interval time.Duration) bool {
	t := time.NewTimer(interval)

	select {
	case <-t.C:
		return true

	case <-discovery.stop:
		if !t.Stop() {
			<-t.C
		}
		return false
	}
}

/**
 * Stop discovery
 */
func (discovery *Discovery) Stop() {
	discovery.stop <- true
}

/**
 * Returns backends channel
 */
func (discovery *Discovery) Discover() <-chan []core.Backend {
	return discovery.out
}
