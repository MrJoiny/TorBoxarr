package torbox

import "context"

// MockClient implements the Client interface for testing.
var _ Client = (*MockClient)(nil)

type MockClient struct {
	CreateTorrentTaskFn func(ctx context.Context, req CreateTorrentTaskRequest) (*CreateTaskResponse, error)
	CreateUsenetTaskFn  func(ctx context.Context, req CreateUsenetTaskRequest) (*CreateTaskResponse, error)
	GetQueuedStatusFn   func(ctx context.Context, sourceType, queuedID string) (*TaskStatus, error)
	GetTaskStatusFn     func(ctx context.Context, sourceType, remoteID string) (*TaskStatus, error)
	FindActiveTaskFn    func(ctx context.Context, sourceType, remoteID, queueAuthID, remoteHash string) (*TaskStatus, error)
	GetDownloadLinksFn  func(ctx context.Context, sourceType, remoteID string) ([]DownloadAsset, error)
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
	return nil, nil
}

func (m *MockClient) GetDownloadLinks(ctx context.Context, sourceType, remoteID string) ([]DownloadAsset, error) {
	if m.GetDownloadLinksFn != nil {
		return m.GetDownloadLinksFn(ctx, sourceType, remoteID)
	}
	return nil, nil
}
