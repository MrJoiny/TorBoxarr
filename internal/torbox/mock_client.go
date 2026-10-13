package torbox

import "context"

// MockClient implements the Client interface for testing.
var _ Client = (*MockClient)(nil)

type MockClient struct {
	CreateTorrentTaskFn        func(ctx context.Context, req CreateTorrentTaskRequest) (*CreateTaskResponse, error)
	CreateUsenetTaskFn         func(ctx context.Context, req CreateUsenetTaskRequest) (*CreateTaskResponse, error)
	GetQueuedStatusFn          func(ctx context.Context, sourceType, queuedID string) (*TaskStatus, error)
	FindQueuedTaskFn           func(ctx context.Context, sourceType, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetTaskStatusFn            func(ctx context.Context, sourceType, remoteID string) (*TaskStatus, error)
	FindActiveTaskFn           func(ctx context.Context, sourceType, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error)
	FindActiveTaskByIdentityFn func(ctx context.Context, sourceType, remoteID, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetDownloadLinksFn         func(ctx context.Context, sourceType, remoteID string) ([]DownloadAsset, error)
	DeleteTaskFn               func(ctx context.Context, sourceType, remoteID string) error
	DeleteQueuedTaskFn         func(ctx context.Context, sourceType, queuedID string) error
}

func (m *MockClient) CreateTorrentTask(ctx context.Context, req CreateTorrentTaskRequest) (*CreateTaskResponse, error) {
	if m.CreateTorrentTaskFn != nil {
		return m.CreateTorrentTaskFn(ctx, req)
	}
	return &CreateTaskResponse{}, nil
}

func (m *MockClient) CreateUsenetTask(ctx context.Context, req CreateUsenetTaskRequest) (*CreateTaskResponse, error) {
	if m.CreateUsenetTaskFn != nil {
		return m.CreateUsenetTaskFn(ctx, req)
	}
	return &CreateTaskResponse{}, nil
}

func (m *MockClient) GetQueuedStatus(ctx context.Context, sourceType, queuedID string) (*TaskStatus, error) {
	if m.GetQueuedStatusFn != nil {
		return m.GetQueuedStatusFn(ctx, sourceType, queuedID)
	}
	return nil, nil
}

func (m *MockClient) FindQueuedTask(ctx context.Context, sourceType, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	if m.FindQueuedTaskFn != nil {
		return m.FindQueuedTaskFn(ctx, sourceType, queuedID, queueAuthID, remoteHash)
	}
	// Keep the zero-config mock useful for tests that only care about the
	// deletion request. Real clients always perform the lookup themselves.
	if queuedID != "" {
		return &TaskStatus{QueuedID: queuedID, QueueAuthID: queueAuthID, Hash: remoteHash}, nil
	}
	return nil, nil
}

func (m *MockClient) GetTaskStatus(ctx context.Context, sourceType, remoteID string) (*TaskStatus, error) {
	if m.GetTaskStatusFn != nil {
		return m.GetTaskStatusFn(ctx, sourceType, remoteID)
	}
	return nil, nil
}

func (m *MockClient) FindActiveTask(ctx context.Context, sourceType, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	if m.FindActiveTaskFn != nil {
		return m.FindActiveTaskFn(ctx, sourceType, remoteID, queueAuthID, remoteHash)
	}
	return m.findActiveTask(remoteID, "", queueAuthID, remoteHash), nil
}

func (m *MockClient) FindActiveTaskByIdentity(ctx context.Context, sourceType, remoteID, queuedID, queueAuthID, remoteHash string) (*TaskStatus, error) {
	if m.FindActiveTaskByIdentityFn != nil {
		return m.FindActiveTaskByIdentityFn(ctx, sourceType, remoteID, queuedID, queueAuthID, remoteHash)
	}
	if m.FindActiveTaskFn != nil {
		return m.FindActiveTaskFn(ctx, sourceType, remoteID, queueAuthID, remoteHash)
	}
	if remoteID != "" {
		return &TaskStatus{RemoteID: remoteID, QueueAuthID: queueAuthID, Hash: remoteHash}, nil
	}
	return nil, nil
}

func (m *MockClient) findActiveTask(remoteID, queuedID, queueAuthID, remoteHash string) *TaskStatus {
	if remoteID != "" {
		return &TaskStatus{RemoteID: remoteID, QueuedID: queuedID, QueueAuthID: queueAuthID, Hash: remoteHash}
	}
	if queuedID != "" || queueAuthID != "" || remoteHash != "" {
		return &TaskStatus{QueuedID: queuedID, QueueAuthID: queueAuthID, Hash: remoteHash}
	}
	return nil
}

func (m *MockClient) GetDownloadLinks(ctx context.Context, sourceType, remoteID string) ([]DownloadAsset, error) {
	if m.GetDownloadLinksFn != nil {
		return m.GetDownloadLinksFn(ctx, sourceType, remoteID)
	}
	return nil, nil
}

func (m *MockClient) DeleteTask(ctx context.Context, sourceType, remoteID string) error {
	if m.DeleteTaskFn != nil {
		return m.DeleteTaskFn(ctx, sourceType, remoteID)
	}
	return nil
}

func (m *MockClient) DeleteQueuedTask(ctx context.Context, sourceType, queuedID string) error {
	if m.DeleteQueuedTaskFn != nil {
		return m.DeleteQueuedTaskFn(ctx, sourceType, queuedID)
	}
	return nil
}
