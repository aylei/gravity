package position_store

import (
	"sync"
	"time"

	"github.com/juju/errors"
	"github.com/moiot/gravity/pkg/config"
	log "github.com/sirupsen/logrus"
)

type Position struct {
	Name       string
	Stage      config.InputMode
	Value      string
	UpdateTime time.Time
}

func (p Position) Validate() error {
	if p.Stage != config.Stream && p.Stage != config.Batch {
		return errors.Errorf("invalid position stage: %v", p.Stage)
	}

	if p.Value == "" {
		return errors.Errorf("invalid position value: %v", p.Value)
	}

	if p.UpdateTime.IsZero() {
		return errors.Errorf("invalid zero position update time")
	}
	return nil
}

type PositionCacheInterface interface {
	Start() error
	Close()
	Put(position Position) error
	Get() (position Position, exist bool, err error)
	Flush() error
	Clear() error
}

type defaultPositionCache struct {
	pipelineName string
	exist        bool
	dirty        bool
	repo         PositionRepo

	position Position
	sync.Mutex

	closeC chan struct{}
	wg     sync.WaitGroup
}

func (cache *defaultPositionCache) Start() error {
	cache.wg.Add(1)
	go func() {
		defer cache.wg.Done()
		ticker := time.NewTicker(time.Second * 5)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := cache.Flush(); err != nil {
					log.Fatalf("[defaultPositionCache] ticker flush failed: %v", errors.ErrorStack(err))
				}
			case <-cache.closeC:
				if err := cache.Flush(); err != nil {
					log.Fatalf("[defaultPositionCache] close flush failed: %v", errors.ErrorStack(err))
				}
				return
			}
		}
	}()
	return nil
}

func (cache *defaultPositionCache) Close() {
	log.Infof("[defaultPositionCache] closing")
	close(cache.closeC)
	cache.wg.Wait()
	log.Infof("[defaultPositionCache] closed")
}

func (cache *defaultPositionCache) Put(position Position) error {
	cache.Lock()
	defer cache.Unlock()
	if err := position.Validate(); err != nil {
		return errors.Trace(err)
	}
	cache.position = position
	cache.dirty = true
	return nil
}

func (cache *defaultPositionCache) Get() (Position, bool, error) {
	cache.Lock()
	defer cache.Unlock()

	if !cache.exist {
		position, exist, err := cache.repo.Get(cache.pipelineName)
		if err != nil && exist {
			cache.exist = true
		}
		return position, true, errors.Trace(err)
	}

	if err := cache.position.Validate(); err != nil {
		return Position{}, true, errors.Trace(err)
	}
	return cache.position, true, nil
}

func (cache *defaultPositionCache) Flush() error {
	cache.Lock()
	defer cache.Unlock()

	if !cache.dirty {
		return nil
	}

	err := cache.repo.Put(cache.pipelineName, cache.position)
	if err != nil {
		return errors.Trace(err)
	}
	cache.dirty = false
	return nil
}

func (cache *defaultPositionCache) Clear() error {
	cache.Lock()
	defer cache.Unlock()
	position := Position{
		Name:  cache.pipelineName,
		Stage: config.Unknown,
		Value: "",
	}

	if err := cache.repo.Delete(cache.pipelineName); err != nil {
		return errors.Trace(err)
	}

	cache.position = position
	cache.dirty = false
	cache.exist = false
	return nil
}

func NewPositionCache(pipelineName string, repo PositionRepo) (PositionCacheInterface, error) {
	store := defaultPositionCache{pipelineName: pipelineName, repo: repo, closeC: make(chan struct{})}

	// Load initial data from repo
	position, exist, err := repo.Get(pipelineName)
	if err != nil {
		return nil, errors.Trace(err)
	}
	store.position = position
	store.exist = exist

	return &store, nil
}
