package main

import (
	"fmt"
	"sync"
	"time"
)

// AlarmManager 管理告警池和分析任务
type AlarmManager struct {
	pool      *AlarmPool
	taskQueue chan *Alarm
	mu        sync.RWMutex
	stopCh    chan struct{}
}

// NewAlarmManager 创建告警管理器
func NewAlarmManager(pool *AlarmPool) *AlarmManager {
	return &AlarmManager{
		pool:      pool,
		taskQueue: make(chan *Alarm, 100),
		stopCh:    make(chan struct{}),
	}
}

// Start 启动告警管理器
func (am *AlarmManager) Start() {
	go am.processAlarms()
}

// Stop 停止告警管理器
func (am *AlarmManager) Stop() {
	close(am.stopCh)
}

// ReceiveAlarm 接收告警
func (am *AlarmManager) ReceiveAlarm(alarm Alarm) {
	am.mu.Lock()
	am.pool.Add(alarm)
	am.mu.Unlock()

	// 将告警放入队列等待处理
	select {
	case am.taskQueue <- &alarm:
	case <-time.After(5 * time.Second):
		fmt.Printf("[WARN] Alarm queue is full, dropping alarm %s\n", alarm.ID)
	}
}

// processAlarms 处理告警流程
func (am *AlarmManager) processAlarms() {
	var pendingAlarms []*Alarm
	timer := time.NewTimer(30 * time.Second)
	timer.Stop()

	for {
		select {
		case alarm := <-am.taskQueue:
			pendingAlarms = append(pendingAlarms, alarm)
			// 重置计时器，等待收集更多告警
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(30 * time.Second)

		case <-timer.C:
			// 倒计时结束，启动分析任务
			if len(pendingAlarms) > 0 {
				fmt.Printf("[INFO] Countdown ended, starting analysis for %d alarms\n", len(pendingAlarms))
				// 这里会触发分析流程
				pendingAlarms = nil
			}

		case <-am.stopCh:
			return
		}
	}
}

// GetAlarmPool 获取告警池
func (am *AlarmManager) GetAlarmPool() *AlarmPool {
	am.mu.RLock()
	defer am.mu.RUnlock()
	return am.pool
}

// CreateMockAlarms 创建模拟告警数据
func CreateMockAlarms() []Alarm {
	now := time.Now()
	return []Alarm{
		{
			ID:          "ALERT-001",
			Level:       "critical",
			Title:       "API 响应时间异常",
			Description: "支付接口响应时间超过阈值 (P99 > 2000ms)",
			Service:     "payment-service",
			Metrics: map[string]float64{
				"p50_latency":    850.0,
				"p99_latency":    2340.0,
				"error_rate":     0.05,
				"request_rate":   1200.0,
				"cpu_usage":      78.0,
				"memory_usage":   65.0,
			},
			Labels: map[string]string{
				"region":   "us-east-1",
				"instance": "payment-pod-123",
				"env":      "production",
			},
			Timestamp: now.Add(-5 * time.Minute),
			Metadata: map[string]any{
				"affected_users": 1500,
				"transaction_volume": "high",
			},
		},
		{
			ID:          "ALERT-002",
			Level:       "warning",
			Title:       "数据库连接数接近上限",
			Description: "PostgreSQL 连接数使用率达到 85%",
			Service:     "payment-db",
			Metrics: map[string]float64{
				"connections":        170.0,
				"max_connections":    200.0,
				"connection_usage":   0.85,
				"query_latency_p99":  450.0,
				"db_cpu_usage":       68.0,
			},
			Labels: map[string]string{
				"region":     "us-east-1",
				"database":   "payment-primary",
				"instance":   "db-001",
			},
			Timestamp: now.Add(-3 * time.Minute),
			Metadata: map[string]any{
				"slow_queries": 45,
			},
		},
		{
			ID:          "ALERT-003",
			Level:       "warning",
			Title:       "内存使用率上升",
			Description: "Payment Service 内存使用率持续上升",
			Service:     "payment-service",
			Metrics: map[string]float64{
				"memory_usage":  78.0,
				"memory_limit":  1024.0,
				"memory_used":   798.0,
				"gc_frequency":  12.0,
			},
			Labels: map[string]string{
				"region":   "us-east-1",
				"instance": "payment-pod-123",
			},
			Timestamp: now.Add(-2 * time.Minute),
		},
	}
}
