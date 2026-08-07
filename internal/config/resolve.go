package config

// DerivedQueueCapacity returns the default queue capacity for a worker count.
func DerivedQueueCapacity(workerCount int) int {
	if workerCount < 1 {
		workerCount = 1
	}
	return workerCount * 8
}

// ApplyCLIOverrides applies serve/check CLI flags after YAML load.
// When workers is overridden and queue capacity was still derived from defaults,
// queue.capacity is recomputed as workers.count * 8.
func (c *Config) ApplyCLIOverrides(host string, port, workers int) {
	if host != "" {
		c.Server.Host = host
	}
	if port > 0 {
		c.Server.Port = port
	}
	if workers > 0 {
		if c.QueueCapacityDerived {
			c.Queue.Capacity = DerivedQueueCapacity(workers)
		}
		c.Workers.Count = workers
	}
}
