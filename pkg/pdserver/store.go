// Copyright 2016 DeepFabric, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package pdserver

import (
	"context"
	"time"

	"github.com/coreos/etcd/clientv3"
	"github.com/coreos/etcd/embed"
	"github.com/deepfabric/elasticell/pkg/log"
	"github.com/deepfabric/elasticell/pkg/pb/metapb"
	"github.com/deepfabric/elasticell/pkg/pb/pdpb"
	"github.com/pkg/errors"
)

const (
	// DefaultTimeout default timeout
	DefaultTimeout = time.Second * 3
	// DefaultRequestTimeout default request timeout
	DefaultRequestTimeout = 10 * time.Second
	// DefaultSlowRequestTime default slow request time
	DefaultSlowRequestTime = time.Second * 1

	pdRootPath         = "/pd"
	pdIDPath           = "/pd/meta/id"
	pdLeaderPath       = "/pd/meta/leader"
	pdClusterIDPath    = "/pd/meta/cluster_id"
	pdBootstrappedPath = "/pd/meta/boot"
	pdClusterRootPath  = "/pd/cluster"
)

// ClusterStore is the store interface for cluster info
type ClusterStore interface {
	GetCurrentClusterMembers() (*clientv3.MemberListResponse, error)
	GetClusterID() (uint64, error)
	CreateFirstClusterID() (uint64, error)
	SetClusterBootstrapped(clusterID uint64, cluster metapb.Cluster, store metapb.Store, cell metapb.Cell) (bool, error)
	LoadClusterMeta(clusterID uint64) (*metapb.Cluster, error)
	LoadStoreMeta(clusterID uint64, limit int64, do func(metapb.Store)) error
	LoadCellMeta(clusterID uint64, limit int64, do func(metapb.Cell)) error
	SetStoreMeta(clusterID uint64, store metapb.Store) error
	SetCellMeta(clusterID uint64, cell metapb.Cell) error
}

// LeaderStore is the store interface for leader info
type LeaderStore interface {
	CampaignLeader(leaderSignature string, leaderLeaseTTL int64, enableLeaderFun func()) error
	WatchLeader()
	ResignLeader(leaderSignature string) error
	GetCurrentLeader() (*pdpb.Leader, error)
}

// IDStore is the store interface for id info
type IDStore interface {
	GetID() (uint64, error)
	CreateID(leaderSignature string, value uint64) error
	UpdateID(leaderSignature string, old, value uint64) error
}

// Store is the store interface for all pd store info
type Store interface {
	ClusterStore
	IDStore
	LeaderStore

	Close() error
}

// Store used for  metedata
type pdStore struct {
	client *clientv3.Client
}

// NewStore create a store
func NewStore(cfg *embed.Config) (Store, error) {
	c, err := initEctdClient(cfg)
	if err != nil {
		return nil, err
	}

	s := new(pdStore)
	s.client = c
	return s, nil
}

func initEctdClient(cfg *embed.Config) (*clientv3.Client, error) {
	endpoints := []string{cfg.LCUrls[0].String()}

	log.Infof("bootstrap: create etcd v3 client, endpoints=<%v>", endpoints)

	client, err := clientv3.New(clientv3.Config{
		Endpoints:   endpoints,
		DialTimeout: DefaultTimeout,
	})
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	return client, nil
}

// Close close ectd client
func (s *pdStore) Close() error {
	if s.client != nil {
		return s.client.Close()
	}

	return nil
}

func (s *pdStore) getValue(key string, opts ...clientv3.OpOption) ([]byte, error) {
	resp, err := s.get(key, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "")
	}

	if n := len(resp.Kvs); n == 0 {
		return nil, nil
	} else if n > 1 {
		return nil, errors.Errorf("invalid get value resp %v, must only one", resp.Kvs)
	}

	return resp.Kvs[0].Value, nil
}

func (s *pdStore) get(key string, opts ...clientv3.OpOption) (*clientv3.GetResponse, error) {
	ctx, cancel := context.WithTimeout(s.client.Ctx(), DefaultRequestTimeout)
	defer cancel()

	start := time.Now()
	resp, err := clientv3.NewKV(s.client).Get(ctx, key, opts...)
	if err != nil {
		log.Errorf("embed-ectd: read option failure, key=<%s>, errors:\n %+v",
			key,
			err)
		return resp, errors.Wrap(err, "")
	}

	if cost := time.Since(start); cost > DefaultSlowRequestTime {
		log.Warnf("embed-ectd: read option is too slow, key=<%s>, cost=<%d>",
			key,
			cost)
	}

	return resp, nil
}

func (s *pdStore) save(key, value string) error {
	resp, err := s.txn().Then(clientv3.OpPut(key, value)).Commit()
	if err != nil {
		return errors.Wrap(err, "")
	}

	if !resp.Succeeded {
		return errors.Wrap(errTxnFailed, "")
	}

	return nil
}