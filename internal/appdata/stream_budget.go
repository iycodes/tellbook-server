package appdata

import (
	"errors"
	"strings"
	"sync"
)

var ErrStreamBudgetExceeded = errors.New("stream connection budget exceeded")

// StreamBudget is a process-local admission guard. PostgreSQL remains the
// durable source of truth; this only protects one API instance from accepting
// more long-lived sockets than it was provisioned to hold.
type StreamBudget struct {
	maxTotal    int
	maxRemote   int
	maxResource int

	mu        sync.Mutex
	total     int
	remote    map[string]int
	resources map[string]int
}

func NewStreamBudget(maxTotal, maxRemote, maxResource int) *StreamBudget {
	return &StreamBudget{
		maxTotal: maxTotal, maxRemote: maxRemote, maxResource: maxResource,
		remote: make(map[string]int), resources: make(map[string]int),
	}
}

func (budget *StreamBudget) Acquire(remote, resource string) (func(), error) {
	if budget == nil {
		return func() {}, nil
	}
	remote = strings.TrimSpace(remote)
	resource = strings.TrimSpace(resource)
	budget.mu.Lock()
	if (budget.maxTotal > 0 && budget.total >= budget.maxTotal) ||
		(remote != "" && budget.maxRemote > 0 && budget.remote[remote] >= budget.maxRemote) ||
		(resource != "" && budget.maxResource > 0 && budget.resources[resource] >= budget.maxResource) {
		budget.mu.Unlock()
		return nil, ErrStreamBudgetExceeded
	}
	budget.total++
	if remote != "" {
		budget.remote[remote]++
	}
	if resource != "" {
		budget.resources[resource]++
	}
	budget.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			budget.mu.Lock()
			budget.total--
			if remote != "" {
				budget.remote[remote]--
				if budget.remote[remote] <= 0 {
					delete(budget.remote, remote)
				}
			}
			if resource != "" {
				budget.resources[resource]--
				if budget.resources[resource] <= 0 {
					delete(budget.resources, resource)
				}
			}
			budget.mu.Unlock()
		})
	}, nil
}
