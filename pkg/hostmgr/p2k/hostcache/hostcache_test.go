// Copyright (c) 2019 Uber Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hostcache

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uber/peloton/.gen/peloton/api/v1alpha/peloton"
	"github.com/uber/peloton/.gen/peloton/api/v1alpha/pod"
	hostmgr "github.com/uber/peloton/.gen/peloton/private/hostmgr/v1alpha"
	"github.com/uber/peloton/pkg/hostmgr/scalar"

	"github.com/pborman/uuid"
	"github.com/stretchr/testify/require"
)

// TestAcquireLeases tests the host cache AcquireLeases API
func (suite *HostCacheTestSuite) TestAcquireLeases() {
	testTable := map[string]struct {
		filter           *hostmgr.HostFilter
		allocatedPerHost scalar.Resources
		matched          int
		filterCounts     map[string]uint32
	}{
		// match all 10 hosts because
		"acquire-all": {
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   2.0,
						MemLimitMb: 2.0,
					},
				},
			},
			allocatedPerHost: scalar.Resources{},
			matched:          10,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_MATCH"): 10,
			},
		},
		// num of actual matching hosts > max limit in the filter, should
		// match MaxHosts only
		"acquire-match-maxlimit": {
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   2.0,
						MemLimitMb: 2.0,
					},
				},
				MaxHosts: 5,
			},
			allocatedPerHost: scalar.Resources{},
			matched:          5,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_MATCH"): 5,
			},
		},

		// there is 0 allocation on each host but the resource constraint needs
		// a lot more resources
		"filter-match-none-high-demand": {
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   100.0,
						MemLimitMb: 100.0,
					},
				},
			},
			allocatedPerHost: scalar.Resources{},
			matched:          0,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_INSUFFICIENT_RESOURCES"): 10,
			},
		},
		// hosts are heavily allocated so none of them matches the resource
		// constraint
		"filter-match-none-high-allocation": {
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   2.0,
						MemLimitMb: 20.0,
					},
				},
			},
			// Each host is allocated 9 CPU and 90Mem
			// only available resource is 1 CPU and 10Mem per host
			// demand is 2 CPU and 20Mem resulting in no match
			allocatedPerHost: createResource(9.0, 90.0),
			matched:          0,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_INSUFFICIENT_RESOURCES"): 10,
			},
		},
	}
	for ttName, tt := range testTable {
		// Generate 10 host summary with 10 CPU and 100 Mem per host which
		// are in ReadyHost state
		hosts := generateHostSummaries(10)
		hc := &hostCache{
			hostIndex: make(map[string]HostSummary),
		}
		// initialize host cache with these 10 hosts
		for _, s := range hosts {
			s.(*baseHostSummary).allocated = tt.allocatedPerHost
			s.(*baseHostSummary).available = s.(*baseHostSummary).capacity.Subtract(tt.allocatedPerHost)
			hc.hostIndex[s.GetHostname()] = s
		}

		leases, filterResult := hc.AcquireLeases(tt.filter)

		suite.Equal(tt.matched, len(leases), "test case %s", ttName)
		suite.Equal(tt.filterCounts, filterResult, "test case %s", ttName)
	}
}

// TestGetClusterCapacity tests the host cache GetClusterCapacity API
func (suite *HostCacheTestSuite) TestGetClusterCapacity() {
	hosts := generateHostSummaries(10)
	hc := &hostCache{
		hostIndex: make(map[string]HostSummary),
	}

	// Allocate 1CPU and 10Mem per host
	allocPerHost := createResource(1.0, 10.0)
	// initialize host cache with these 10 hosts
	for _, s := range hosts {
		s.(*baseHostSummary).allocated = allocPerHost
		hc.hostIndex[s.GetHostname()] = s
	}

	// Exepect total capacity of 100CPU and 100MemMb
	expectedCapacity := createResource(100.0, 1000.0)
	// Exepect total allocation of 10CPU and 100MemMb
	expectedAllocation := createResource(10.0, 100.0)

	capacity, allocation := hc.GetClusterCapacity()
	suite.Equal(expectedCapacity, capacity)
	suite.Equal(expectedAllocation, allocation)
}

// TestMarshal tests the host cache GetSummaries API.
func (suite *HostCacheTestSuite) TestGetSummaries() {
	hosts := generateHostSummaries(10)
	hc := &hostCache{
		hostIndex: make(map[string]HostSummary),
	}

	// Allocate 1CPU and 10Mem per host
	allocPerHost := createResource(1.0, 10.0)
	// initialize host cache with these 10 hosts
	for _, s := range hosts {
		s.(*baseHostSummary).allocated = allocPerHost
		hc.hostIndex[s.GetHostname()] = s
	}

	for _, summary := range hc.GetSummaries() {
		host := hc.hostIndex[summary.GetHostname()].(*baseHostSummary)
		suite.Equal(summary, host)
	}
}

// TestTerminateLease tests hostcache TerminateLease API
func (suite *HostCacheTestSuite) TestTerminateLease() {
	testTable := map[string]struct {
		podToResMap  map[string]scalar.Resources
		filter       *hostmgr.HostFilter
		matched      int
		filterCounts map[string]uint32

		errExpected bool
		errMsg      string
	}{
		"terminate-valid-lease": {
			errExpected: false,
			// launch 5 pods each with 1 Cpu and 10 Mem
			// So total requirement is 5 CPU and 50Mem
			podToResMap: generatePodToResMap(10, 1.0, 10.0),
			// filter to match hosts that have 5 CPU and 50Mem
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   5.0,
						MemLimitMb: 50.0,
					},
				},
			},
			matched: 1,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_MATCH"): 1,
			},
		},
	}
	for ttName, tt := range testTable {
		// Generate 1 host summary with 10 CPU and 100 Mem
		hosts := generateHostSummaries(1)
		hc := &hostCache{
			hostIndex: make(map[string]HostSummary),
		}
		// initialize host cache with this host
		for _, s := range hosts {
			hc.hostIndex[s.GetHostname()] = s
		}

		leases, filterResult := hc.AcquireLeases(tt.filter)

		suite.Equal(tt.matched, len(leases), "test case %s", ttName)
		suite.Equal(tt.filterCounts, filterResult, "test case %s", ttName)

		// Now Terminate this lease and make sure the error that may
		// result is expected.
		for _, lease := range leases {
			err := hc.TerminateLease(
				lease.GetHostSummary().GetHostname(),
				lease.GetLeaseId().GetValue(),
			)
			if tt.errExpected {
				suite.Equal(tt.errMsg, err.Error(), "test case %s", ttName)
				continue
			}
			suite.NoError(err, "test case %s", ttName)

			err = hc.CompleteLease(
				lease.GetHostSummary().GetHostname(),
				lease.GetLeaseId().GetValue(),
				tt.podToResMap,
			)
			suite.Error(err, "test case %s", ttName)
		}
	}
}

// TestCompleteLease tests hostcache CompleteLease API
func (suite *HostCacheTestSuite) TestCompleteLease() {
	testTable := map[string]struct {
		errExpected  bool
		errMsg       string
		podToResMap  map[string]scalar.Resources
		filter       *hostmgr.HostFilter
		matched      int
		invalidLease bool
		filterCounts map[string]uint32
	}{
		"complete-valid-lease": {
			errExpected: false,
			// launch 5 pods each with 1 Cpu and 10 Mem
			// So total requirement is 5 CPU and 50Mem
			podToResMap: generatePodToResMap(10, 1.0, 10.0),
			// filter to match hosts that have 5 CPU and 50Mem
			filter: &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   5.0,
						MemLimitMb: 50.0,
					},
				},
			},
			matched: 1,
			filterCounts: map[string]uint32{
				strings.ToLower("HOST_FILTER_MATCH"): 1,
			},
		},
	}
	for ttName, tt := range testTable {
		// Generate 1 host summary with 10 CPU and 100 Mem
		hosts := generateHostSummaries(1)
		hc := &hostCache{
			hostIndex: make(map[string]HostSummary),
		}
		// initialize host cache with this host
		for _, s := range hosts {
			hc.hostIndex[s.GetHostname()] = s
		}

		leases, filterResult := hc.AcquireLeases(tt.filter)

		suite.Equal(tt.matched, len(leases), "test case %s", ttName)
		suite.Equal(tt.filterCounts, filterResult, "test case %s", ttName)

		// Now Complete this lease with the podMap which you want to launch
		for _, lease := range leases {
			err := hc.CompleteLease(
				lease.GetHostSummary().GetHostname(),
				lease.GetLeaseId().GetValue(),
				tt.podToResMap,
			)
			suite.NoError(err, "test case %s", ttName)
		}
	}
}

// TestCompleteLeaseErrors tests the error conditions for CompleteLease
func (suite *HostCacheTestSuite) TestCompleteLeaseErrors() {
	// Generate 1 host summary with 10 CPU and 100 Mem
	hosts := generateHostSummaries(1)
	hc := &hostCache{
		hostIndex: make(map[string]HostSummary),
	}
	// initialize host cache with this host
	for _, s := range hosts {
		hc.hostIndex[s.GetHostname()] = s
	}

	podToResMap := generatePodToResMap(5, 1.0, 10.0)
	// filter to match hosts that have 5 CPU and 50Mem
	filter := &hostmgr.HostFilter{
		ResourceConstraint: &hostmgr.ResourceConstraint{
			Minimum: &pod.ResourceSpec{
				CpuLimit:   5.0,
				MemLimitMb: 50.0,
			},
		},
	}
	leases, _ := hc.AcquireLeases(filter)
	suite.Equal(1, len(leases))

	testTable := map[string]struct {
		errMsg     string
		leaseID    string
		hostname   string
		notPlacing bool
	}{
		"complete-invalid-lease-id": {
			errMsg:   fmt.Sprintf("code:invalid-argument message:host leaseID does not match"),
			leaseID:  _leaseID,
			hostname: leases[0].GetHostSummary().GetHostname(),
		},
		"complete-invalid-hostname": {
			errMsg:   fmt.Sprintf("code:not-found message:cannot find host %v in cache", _hostname),
			leaseID:  leases[0].GetLeaseId().GetValue(),
			hostname: _hostname,
		},
		"complete-invalid-host-status": {
			notPlacing: true,
			errMsg:     fmt.Sprintf("code:invalid-argument message:host status is not Placing"),
			leaseID:    leases[0].GetLeaseId().GetValue(),
			hostname:   leases[0].GetHostSummary().GetHostname(),
		},
	}
	for ttName, tt := range testTable {
		if tt.notPlacing {
			// mark the host as ready
			hosts[0].CasStatus(PlacingHost, ReadyHost)
		}

		err := hc.CompleteLease(
			tt.hostname,
			tt.leaseID,
			podToResMap,
		)

		suite.Error(err, "test case %s", ttName)
		suite.Equal(tt.errMsg, err.Error(), "test case %s", ttName)

		if tt.notPlacing {
			// mark the host back to placing state
			hosts[0].CasStatus(ReadyHost, PlacingHost)
		}
	}
}

// TestAcquireLeasesParallel tests acquiring host leases from host cache in
// parallel. This test will initialize host cache with 255 hosts and create
// 8 threads which will try to acquire 2^(threadnum) hosts
func (suite *HostCacheTestSuite) TestAcquireLeasesParallel() {
	// Generate 255 host summaries with 10 CPU and 100 Mem
	numHosts := 255
	hosts := generateHostSummaries(numHosts)
	hc := &hostCache{
		hostIndex: make(map[string]HostSummary),
	}
	// initialize host cache with this host
	for _, s := range hosts {
		hc.hostIndex[s.GetHostname()] = s
	}

	var aggrLeases []*hostmgr.HostLease
	nClients := 8
	mutex := &sync.Mutex{}
	wg := sync.WaitGroup{}
	wg.Add(nClients)

	for i := 0; i < nClients; i++ {
		go func(i float64) {
			// Run AcquireLeases with MaxHosts set to 2^i
			// So thread 0 will acquire 1 host
			// thread 1 will acquire 2 hosts
			// thread 2 will acquire 4 hosts
			// thread 3 will acquire 8 hosts
			// and so on ...
			maxHosts := uint32(math.Pow(2, i))
			filter := &hostmgr.HostFilter{
				ResourceConstraint: &hostmgr.ResourceConstraint{
					Minimum: &pod.ResourceSpec{
						CpuLimit:   5.0,
						MemLimitMb: 50.0,
					},
				},
				MaxHosts: maxHosts,
			}
			leases, results := hc.AcquireLeases(filter)
			mutex.Lock()
			defer mutex.Unlock()
			suite.Equal(int(maxHosts), len(leases))
			matched, ok := results[strings.ToLower("HOST_FILTER_MATCH")]
			suite.True(ok)
			suite.Equal(maxHosts, matched)
			aggrLeases = append(aggrLeases, leases...)
			wg.Done()
		}(float64(i))
	}
	wg.Wait()
	// number of leases acquired should be equal to numHosts
	suite.Equal(numHosts, len(aggrLeases))
}

// TODO: move to use mock after host summary is moved to a different package.
func TestHoldForPods(t *testing.T) {
	require := require.New(t)
	hs := generateHostSummaries(1)[0]
	podID := &peloton.PodID{Value: uuid.New()}
	hc := &hostCache{
		hostIndex:    map[string]HostSummary{hs.GetHostname(): hs},
		podHeldIndex: map[string]string{},
	}
	require.Empty(hc.podHeldIndex)
	require.NoError(hc.HoldForPods(hs.GetHostname(), []*peloton.PodID{podID}))
	require.Equal(1, len(hc.podHeldIndex))
	require.Equal(hs.GetHostname(), hc.GetHostHeldForPod(podID))
}

// TODO: move to use mock after host summary is moved to a different package.
func TestHoldForPodsDuplicated(t *testing.T) {
	require := require.New(t)
	hosts := generateHostSummaries(2)
	podID := &peloton.PodID{Value: uuid.New()}
	hc := &hostCache{
		hostIndex: map[string]HostSummary{
			hosts[0].GetHostname(): hosts[0],
			hosts[1].GetHostname(): hosts[1],
		},
		podHeldIndex: map[string]string{},
	}
	require.Empty(hc.podHeldIndex)
	require.NoError(hc.HoldForPods(hosts[0].GetHostname(), []*peloton.PodID{podID}))
	require.Equal(1, len(hc.podHeldIndex))
	require.Equal(hosts[0].GetHostname(), hc.GetHostHeldForPod(podID))

	require.NoError(hc.HoldForPods(hosts[1].GetHostname(), []*peloton.PodID{podID}))
	require.Equal(1, len(hc.podHeldIndex))
	require.Equal(hosts[1].GetHostname(), hc.GetHostHeldForPod(podID))
}

// TODO: move to use mock after host summary is moved to a different package.
func TestReleaseHoldForPods(t *testing.T) {
	require := require.New(t)
	hs := generateHostSummaries(1)[0]
	podID := &peloton.PodID{Value: uuid.New()}
	hc := &hostCache{
		hostIndex:    map[string]HostSummary{hs.GetHostname(): hs},
		podHeldIndex: map[string]string{podID.GetValue(): hs.GetHostname()},
	}
	require.Equal(1, len(hc.podHeldIndex))
	require.NoError(hc.ReleaseHoldForPods(hs.GetHostname(), []*peloton.PodID{podID}))
	require.Empty(hc.podHeldIndex)
}

// TODO: move to use mock after host summary is moved to a different package.
func TestResetExpiredHeldHostSummaries(t *testing.T) {
	require := require.New(t)
	hs := generateHostSummaries(1)[0]
	podID := &peloton.PodID{Value: uuid.New()}
	hc := &hostCache{
		hostIndex:    map[string]HostSummary{hs.GetHostname(): hs},
		podHeldIndex: map[string]string{},
	}
	now := time.Now()
	require.NoError(hc.HoldForPods(hs.GetHostname(), []*peloton.PodID{podID}))
	require.Equal(1, len(hc.podHeldIndex))

	ret := hc.ResetExpiredHeldHostSummaries(now.Add(time.Hour))
	require.Equal(1, len(ret))
	require.Equal(hs.GetHostname(), ret[0])
	require.Empty(hc.podHeldIndex)
}
