// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.
//

// Code generated by MockGen. DO NOT EDIT.
// Source: manager.go
//
// Generated by this command:
//
//	mockgen -copyright_file ../../../hack/mockgen_copyright.txt -source=manager.go -destination=mock.go -package=lvm .
//

// Package lvm is a generated GoMock package.
package lvm

import (
	context "context"
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"
)

// MockManager is a mock of Manager interface.
type MockManager struct {
	ctrl     *gomock.Controller
	recorder *MockManagerMockRecorder
	isgomock struct{}
}

// MockManagerMockRecorder is the mock recorder for MockManager.
type MockManagerMockRecorder struct {
	mock *MockManager
}

// NewMockManager creates a new mock instance.
func NewMockManager(ctrl *gomock.Controller) *MockManager {
	mock := &MockManager{ctrl: ctrl}
	mock.recorder = &MockManagerMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockManager) EXPECT() *MockManagerMockRecorder {
	return m.recorder
}

// CreateLogicalVolume mocks base method.
func (m *MockManager) CreateLogicalVolume(ctx context.Context, opts CreateLVOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateLogicalVolume", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// CreateLogicalVolume indicates an expected call of CreateLogicalVolume.
func (mr *MockManagerMockRecorder) CreateLogicalVolume(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateLogicalVolume", reflect.TypeOf((*MockManager)(nil).CreateLogicalVolume), ctx, opts)
}

// CreatePhysicalVolume mocks base method.
func (m *MockManager) CreatePhysicalVolume(ctx context.Context, opts CreatePVOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreatePhysicalVolume", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// CreatePhysicalVolume indicates an expected call of CreatePhysicalVolume.
func (mr *MockManagerMockRecorder) CreatePhysicalVolume(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreatePhysicalVolume", reflect.TypeOf((*MockManager)(nil).CreatePhysicalVolume), ctx, opts)
}

// CreateVolumeGroup mocks base method.
func (m *MockManager) CreateVolumeGroup(ctx context.Context, opts CreateVGOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateVolumeGroup", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// CreateVolumeGroup indicates an expected call of CreateVolumeGroup.
func (mr *MockManagerMockRecorder) CreateVolumeGroup(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateVolumeGroup", reflect.TypeOf((*MockManager)(nil).CreateVolumeGroup), ctx, opts)
}

// ExtendLogicalVolume mocks base method.
func (m *MockManager) ExtendLogicalVolume(ctx context.Context, opts ExtendLVOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ExtendLogicalVolume", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// ExtendLogicalVolume indicates an expected call of ExtendLogicalVolume.
func (mr *MockManagerMockRecorder) ExtendLogicalVolume(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ExtendLogicalVolume", reflect.TypeOf((*MockManager)(nil).ExtendLogicalVolume), ctx, opts)
}

// GetLogicalVolume mocks base method.
func (m *MockManager) GetLogicalVolume(ctx context.Context, vgName, lvName string) (*LogicalVolume, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetLogicalVolume", ctx, vgName, lvName)
	ret0, _ := ret[0].(*LogicalVolume)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetLogicalVolume indicates an expected call of GetLogicalVolume.
func (mr *MockManagerMockRecorder) GetLogicalVolume(ctx, vgName, lvName any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetLogicalVolume", reflect.TypeOf((*MockManager)(nil).GetLogicalVolume), ctx, vgName, lvName)
}

// GetPhysicalVolume mocks base method.
func (m *MockManager) GetPhysicalVolume(ctx context.Context, pvName string) (*PhysicalVolume, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetPhysicalVolume", ctx, pvName)
	ret0, _ := ret[0].(*PhysicalVolume)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetPhysicalVolume indicates an expected call of GetPhysicalVolume.
func (mr *MockManagerMockRecorder) GetPhysicalVolume(ctx, pvName any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetPhysicalVolume", reflect.TypeOf((*MockManager)(nil).GetPhysicalVolume), ctx, pvName)
}

// GetVolumeGroup mocks base method.
func (m *MockManager) GetVolumeGroup(ctx context.Context, vgName string) (*VolumeGroup, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetVolumeGroup", ctx, vgName)
	ret0, _ := ret[0].(*VolumeGroup)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetVolumeGroup indicates an expected call of GetVolumeGroup.
func (mr *MockManagerMockRecorder) GetVolumeGroup(ctx, vgName any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetVolumeGroup", reflect.TypeOf((*MockManager)(nil).GetVolumeGroup), ctx, vgName)
}

// IsSupported mocks base method.
func (m *MockManager) IsSupported() bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsSupported")
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsSupported indicates an expected call of IsSupported.
func (mr *MockManagerMockRecorder) IsSupported() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsSupported", reflect.TypeOf((*MockManager)(nil).IsSupported))
}

// ListLogicalVolumes mocks base method.
func (m *MockManager) ListLogicalVolumes(ctx context.Context, opts *ListLVOptions) ([]LogicalVolume, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ListLogicalVolumes", ctx, opts)
	ret0, _ := ret[0].([]LogicalVolume)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ListLogicalVolumes indicates an expected call of ListLogicalVolumes.
func (mr *MockManagerMockRecorder) ListLogicalVolumes(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ListLogicalVolumes", reflect.TypeOf((*MockManager)(nil).ListLogicalVolumes), ctx, opts)
}

// ListPhysicalVolumes mocks base method.
func (m *MockManager) ListPhysicalVolumes(ctx context.Context, opts *ListPVOptions) ([]PhysicalVolume, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ListPhysicalVolumes", ctx, opts)
	ret0, _ := ret[0].([]PhysicalVolume)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ListPhysicalVolumes indicates an expected call of ListPhysicalVolumes.
func (mr *MockManagerMockRecorder) ListPhysicalVolumes(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ListPhysicalVolumes", reflect.TypeOf((*MockManager)(nil).ListPhysicalVolumes), ctx, opts)
}

// ListVolumeGroups mocks base method.
func (m *MockManager) ListVolumeGroups(ctx context.Context, opts *ListVGOptions) ([]VolumeGroup, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ListVolumeGroups", ctx, opts)
	ret0, _ := ret[0].([]VolumeGroup)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// ListVolumeGroups indicates an expected call of ListVolumeGroups.
func (mr *MockManagerMockRecorder) ListVolumeGroups(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ListVolumeGroups", reflect.TypeOf((*MockManager)(nil).ListVolumeGroups), ctx, opts)
}

// RemoveLogicalVolume mocks base method.
func (m *MockManager) RemoveLogicalVolume(ctx context.Context, opts RemoveLVOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveLogicalVolume", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveLogicalVolume indicates an expected call of RemoveLogicalVolume.
func (mr *MockManagerMockRecorder) RemoveLogicalVolume(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveLogicalVolume", reflect.TypeOf((*MockManager)(nil).RemoveLogicalVolume), ctx, opts)
}

// RemovePhysicalVolume mocks base method.
func (m *MockManager) RemovePhysicalVolume(ctx context.Context, opts RemovePVOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemovePhysicalVolume", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemovePhysicalVolume indicates an expected call of RemovePhysicalVolume.
func (mr *MockManagerMockRecorder) RemovePhysicalVolume(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemovePhysicalVolume", reflect.TypeOf((*MockManager)(nil).RemovePhysicalVolume), ctx, opts)
}

// RemoveVolumeGroup mocks base method.
func (m *MockManager) RemoveVolumeGroup(ctx context.Context, opts RemoveVGOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RemoveVolumeGroup", ctx, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// RemoveVolumeGroup indicates an expected call of RemoveVolumeGroup.
func (mr *MockManagerMockRecorder) RemoveVolumeGroup(ctx, opts any) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RemoveVolumeGroup", reflect.TypeOf((*MockManager)(nil).RemoveVolumeGroup), ctx, opts)
}
