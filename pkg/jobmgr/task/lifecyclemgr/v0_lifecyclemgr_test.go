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

package lifecyclemgr

import (
	"context"
	"fmt"
	"strings"
	"testing"

	mesos "github.com/uber/peloton/.gen/mesos/v1"
	"github.com/uber/peloton/.gen/peloton/api/v0/peloton"
	"github.com/uber/peloton/.gen/peloton/api/v0/task"
	pbpod "github.com/uber/peloton/.gen/peloton/api/v1alpha/pod"
	v0_hostsvc "github.com/uber/peloton/.gen/peloton/private/hostmgr/hostsvc"
	v0_host_mocks "github.com/uber/peloton/.gen/peloton/private/hostmgr/hostsvc/mocks"

	"github.com/uber/peloton/pkg/common"
	"github.com/uber/peloton/pkg/common/rpc"

	"github.com/golang/mock/gomock"
	"github.com/pborman/uuid"
	"github.com/stretchr/testify/suite"
	"github.com/uber-go/tally"
	"go.uber.org/yarpc"
	"go.uber.org/yarpc/api/transport"
	"go.uber.org/yarpc/yarpcerrors"
	"golang.org/x/time/rate"
)

const randomErrorStr = "random error"

const (
	taskIDFmt            = _testJobID + "-%d-%d"
	testPort             = uint32(100)
	testSecretPath       = "/tmp/secret"
	testSecretStr        = "test-data"
	testTaskConfigData   = "../../../../example/thermos-executor-task-config.bin"
	testAssignedTaskData = "../../../../example/thermos-executor-assigned-task.bin"
	_testJobID           = "bca875f5-322a-4439-b0c9-63e3cf9f982e"
)

type v0LifecycleTestSuite struct {
	suite.Suite
	ctrl        *gomock.Controller
	ctx         context.Context
	mockHostMgr *v0_host_mocks.MockInternalHostServiceYARPCClient
	mesosTaskID string
	jobID       string
	instanceID  int32
	lm          *v0LifecycleMgr
}

func (suite *v0LifecycleTestSuite) TearDownTest() {
	suite.ctrl.Finish()
}

// TestV0LifecycleTestSuite tests V0 LifecycleMgr
func TestV0LifecycleTestSuite(t *testing.T) {
	suite.Run(t, new(v0LifecycleTestSuite))
}

func (suite *v0LifecycleTestSuite) SetupTest() {
	suite.ctrl = gomock.NewController(suite.T())
	suite.ctx = context.Background()
	suite.mockHostMgr = v0_host_mocks.
		NewMockInternalHostServiceYARPCClient(suite.ctrl)
	suite.lm = &v0LifecycleMgr{
		hostManagerV0: suite.mockHostMgr,
		lockState:     &lockState{state: 0},
		metrics:       NewMetrics(tally.NoopScope),
	}
	suite.jobID = "af647b98-0ae0-4dac-be42-c74a524dfe44"
	suite.instanceID = 89
	suite.mesosTaskID = fmt.Sprintf(
		"%s-%d-%s",
		suite.jobID,
		suite.instanceID,
		uuid.New())

}

func createTestTask(instanceID int) *LaunchableTaskInfo {
	tid := fmt.Sprintf(taskIDFmt, instanceID, 2)

	return &LaunchableTaskInfo{
		TaskInfo: &task.TaskInfo{
			JobId: &peloton.JobID{
				Value: _testJobID,
			},
			InstanceId: uint32(instanceID),
			Config: &task.TaskConfig{
				Name: _testJobID,
				Resource: &task.ResourceConfig{
					CpuLimit:    10,
					MemLimitMb:  10,
					DiskLimitMb: 10,
					FdLimit:     10,
				},
				Ports: []*task.PortConfig{
					{
						Name:    "port",
						EnvName: "PORT",
					},
				},
			},
			Runtime: &task.RuntimeInfo{
				MesosTaskId: &mesos.TaskID{
					Value: &tid,
				},
			},
		},
		Spec: &pbpod.PodSpec{
			Containers: []*pbpod.ContainerSpec{
				{
					Name: tid,
					Resource: &pbpod.ResourceSpec{
						CpuLimit:   1.0,
						MemLimitMb: 100.0,
					},
					Ports: []*pbpod.PortSpec{
						{
							Name:  "http",
							Value: 8080,
						},
					},
					Image: "test_image",
				},
			},
		},
	}
}

func (suite *v0LifecycleTestSuite,
) buildKillTasksReq() *v0_hostsvc.KillTasksRequest {
	return &v0_hostsvc.KillTasksRequest{
		TaskIds: []*mesos.TaskID{{Value: &suite.mesosTaskID}},
	}
}

// build a shutdown executor request
func (suite *v0LifecycleTestSuite,
) buildShutdownExecutorsReq() *v0_hostsvc.ShutdownExecutorsRequest {
	return &v0_hostsvc.ShutdownExecutorsRequest{
		Executors: []*v0_hostsvc.ExecutorOnAgent{
			{
				ExecutorId: &mesos.ExecutorID{Value: &suite.mesosTaskID},
				AgentId:    &mesos.AgentID{Value: &suite.mesosTaskID},
			},
		},
	}
}

// TestNew tests creating an instance of v0 lifecyclemgr
func (suite *v0LifecycleTestSuite) TestNew() {
	t := rpc.NewTransport()
	outbound := t.NewOutbound(nil)
	dispatcher := yarpc.NewDispatcher(yarpc.Config{
		Name:     common.PelotonResourceManager,
		Inbounds: nil,
		Outbounds: yarpc.Outbounds{
			common.PelotonHostManager: transport.Outbounds{
				Unary: outbound,
			},
		},
		Metrics: yarpc.MetricsConfig{
			Tally: tally.NoopScope,
		},
	})

	_ = newV0LifecycleMgr(dispatcher, tally.NoopScope)
}

// TestKill tests successful lm.Kill
func (suite *v0LifecycleTestSuite) TestKill() {
	suite.mockHostMgr.EXPECT().
		KillTasks(gomock.Any(), suite.buildKillTasksReq())
	err := suite.lm.Kill(
		suite.ctx,
		suite.mesosTaskID,
		"",
		nil,
	)
	suite.Nil(err)
}

// TestKillLock tests lm.Kill is blocked as expected when it is locked
func (suite *v0LifecycleTestSuite) TestKillLock() {
	suite.lm.LockKill()
	err := suite.lm.Kill(
		suite.ctx,
		suite.mesosTaskID,
		"",
		nil,
	)
	suite.Error(err)
}

// TestKillTaskInvalidTaskIDs tests InvalidTaskIDs error in KillTask
func (suite *v0LifecycleTestSuite) TestKillInvalidTaskIDs() {
	// Simulate InvalidTaskIDs error
	resp := &v0_hostsvc.KillTasksResponse{
		Error: &v0_hostsvc.KillTasksResponse_Error{
			InvalidTaskIDs: &v0_hostsvc.InvalidTaskIDs{
				Message: randomErrorStr,
			},
		},
	}
	suite.mockHostMgr.EXPECT().KillTasks(
		gomock.Any(),
		suite.buildKillTasksReq()).
		Return(resp, nil)
	err := suite.lm.Kill(
		suite.ctx,
		suite.mesosTaskID,
		"",
		nil,
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsInternal(err))
	suite.True(strings.Contains(err.Error(), randomErrorStr))
}

// TestKillFailure tests KillFailure error in Kill
func (suite *v0LifecycleTestSuite) TestKillFailure() {
	// Simulate KillFailure error
	resp := &v0_hostsvc.KillTasksResponse{
		Error: &v0_hostsvc.KillTasksResponse_Error{
			KillFailure: &v0_hostsvc.KillFailure{
				Message: randomErrorStr,
			},
		},
	}
	suite.mockHostMgr.EXPECT().KillTasks(
		gomock.Any(),
		suite.buildKillTasksReq()).
		Return(resp, nil)
	err := suite.lm.Kill(
		suite.ctx,
		suite.mesosTaskID,
		"",
		nil,
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsInternal(err))
	suite.True(strings.Contains(err.Error(), randomErrorStr))
}

// TestKillRateLimit tests task kill fails due to rate limit reached
func (suite *v0LifecycleTestSuite) TestKillRateLimit() {
	err := suite.lm.Kill(
		suite.ctx,
		suite.mesosTaskID,
		"",
		rate.NewLimiter(0, 0),
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsResourceExhausted(err))
}

// TestShutdownExecutorShutdownFailure tests ShutdownFailure error in
// suite.lm.ShutdownExecutor
func (suite *v0LifecycleTestSuite) TestShutdownExecutorShutdownFailure() {
	resp := &v0_hostsvc.ShutdownExecutorsResponse{
		Error: &v0_hostsvc.ShutdownExecutorsResponse_Error{
			ShutdownFailure: &v0_hostsvc.ShutdownFailure{
				Message: randomErrorStr,
			},
		},
	}
	suite.mockHostMgr.EXPECT().ShutdownExecutors(
		suite.ctx,
		suite.buildShutdownExecutorsReq()).
		Return(resp, nil)
	err := suite.lm.ShutdownExecutor(
		suite.ctx,
		suite.mesosTaskID,
		suite.mesosTaskID,
		nil)
	suite.Error(err)
	suite.True(yarpcerrors.IsInternal(err))
	suite.True(strings.Contains(err.Error(), randomErrorStr))
}

// TestShutdownExecutorShutdownLock tests ShutdownFailure is blocked
// as expected when kill is locked
func (suite *v0LifecycleTestSuite) TestShutdownExecutorShutdownLock() {
	suite.lm.LockKill()
	err := suite.lm.ShutdownExecutor(
		suite.ctx,
		suite.mesosTaskID,
		suite.mesosTaskID,
		nil)
	suite.Error(err)
}

// TestExecutorShutdownRateLimit tests executor shutdown fails due to
// rate limit
func (suite *v0LifecycleTestSuite) TestExecutorShutdownRateLimit() {
	err := suite.lm.ShutdownExecutor(
		suite.ctx,
		suite.mesosTaskID,
		suite.mesosTaskID,
		rate.NewLimiter(0, 0),
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsResourceExhausted(err))
}

// TestShutdownExecutorInvalidExecutors tests InvalidExecutors error in
// lm.ShutdownExecutor
func (suite *v0LifecycleTestSuite) TestShutdownExecutorInvalidExecutors() {
	// Simulate InvalidExecutors error
	resp := &v0_hostsvc.ShutdownExecutorsResponse{
		Error: &v0_hostsvc.ShutdownExecutorsResponse_Error{
			InvalidExecutors: &v0_hostsvc.InvalidExecutors{
				Message: randomErrorStr,
			},
		},
	}
	suite.mockHostMgr.EXPECT().ShutdownExecutors(
		suite.ctx, suite.buildShutdownExecutorsReq()).
		Return(resp, nil)
	err := suite.lm.ShutdownExecutor(
		suite.ctx,
		suite.mesosTaskID,
		suite.mesosTaskID,
		nil,
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsInternal(err))
	suite.True(strings.Contains(err.Error(), randomErrorStr))
}

// TestLaunch ensures that multiple tasks can be launched in hostmgr.
func (suite *v0LifecycleTestSuite) TestLaunch() {
	// generate 25 test tasks
	numTasks := 25
	var launchableTasks []*v0_hostsvc.LaunchableTask
	taskInfos := make(map[string]*LaunchableTaskInfo)
	expectedTaskConfigs := make(map[string]*task.TaskConfig)
	for i := 0; i < numTasks; i++ {
		tmp := createTestTask(i)
		launchableTask := v0_hostsvc.LaunchableTask{
			TaskId: tmp.GetRuntime().GetMesosTaskId(),
			Config: tmp.GetConfig(),
			Ports:  tmp.GetRuntime().GetPorts(),
		}
		launchableTasks = append(launchableTasks, &launchableTask)
		taskID := tmp.JobId.Value + "-" + fmt.Sprint(tmp.InstanceId)
		taskInfos[taskID] = tmp
		expectedTaskConfigs[tmp.GetRuntime().GetMesosTaskId().GetValue()] =
			tmp.Config
	}

	expectedHostname := "host-1"
	expectedOfferID := uuid.New()

	// Capture LaunchTasks calls
	launchedTasks := make(map[string]*task.TaskConfig)

	gomock.InOrder(
		// Mock LaunchTasks call.
		suite.mockHostMgr.EXPECT().
			LaunchTasks(
				gomock.Any(),
				gomock.Any()).
			Do(func(_ context.Context, reqBody interface{}) {
				req := reqBody.(*v0_hostsvc.LaunchTasksRequest)
				suite.Equal(req.GetHostname(), expectedHostname)
				suite.Equal(req.GetId().GetValue(), expectedOfferID)
				for _, lt := range req.Tasks {
					launchedTasks[lt.TaskId.GetValue()] = lt.Config
				}
			}).
			Return(&v0_hostsvc.LaunchTasksResponse{}, nil).
			Times(1),
	)

	err := suite.lm.Launch(
		context.Background(),
		expectedOfferID,
		expectedHostname,
		expectedHostname,
		taskInfos,
		nil,
	)

	suite.NoError(err)
	suite.Equal(launchedTasks, expectedTaskConfigs)
}

// TestLaunchErrors tests Launch errors.
func (suite *v0LifecycleTestSuite) TestLaunchErrors() {
	taskInfos := make(map[string]*LaunchableTaskInfo)
	tmp := createTestTask(1)
	taskID := tmp.JobId.Value + "-" + fmt.Sprint(tmp.InstanceId)
	taskInfos[taskID] = tmp

	err := suite.lm.Launch(
		context.Background(),
		"",
		"host",
		"host",
		nil,
		nil,
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsInvalidArgument(err))

	err = suite.lm.Launch(
		context.Background(),
		"",
		"host",
		"host",
		taskInfos,
		rate.NewLimiter(0, 0),
	)
	suite.Error(err)
	suite.True(yarpcerrors.IsResourceExhausted(err))
}

// TestTerminateLease tests successful lm.TerminateLease.
func (suite *v0LifecycleTestSuite) TestTerminateLease() {
	hostname := "test-host-1"
	leaseID := uuid.New()

	suite.mockHostMgr.EXPECT().
		ReleaseHostOffers(gomock.Any(), &v0_hostsvc.ReleaseHostOffersRequest{
			HostOffers: []*v0_hostsvc.HostOffer{{
				Hostname: hostname,
				AgentId:  &mesos.AgentID{Value: &hostname},
				Id:       &peloton.HostOfferID{Value: leaseID},
			}}}).Return(&v0_hostsvc.ReleaseHostOffersResponse{}, nil)

	err := suite.lm.TerminateLease(
		suite.ctx,
		hostname,
		hostname,
		leaseID,
	)
	suite.Nil(err)
}
