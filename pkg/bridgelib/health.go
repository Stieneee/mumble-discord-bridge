package bridgelib

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"
)

// BackoffConfig defines exponential backoff configuration
type BackoffConfig struct {
	InitialDelay time.Duration `json:"initial_delay"`
	MaxDelay     time.Duration `json:"max_delay"`
	Multiplier   float64       `json:"multiplier"`
	Jitter       bool          `json:"jitter"`
}

// DefaultBackoffConfig returns a sensible default backoff configuration
func DefaultBackoffConfig() BackoffConfig {
	return BackoffConfig{
		InitialDelay: 2 * time.Second,
		MaxDelay:     2 * time.Minute,
		Multiplier:   2.0,
		Jitter:       true,
	}
}

// CalculateDelay calculates the delay for a given attempt using exponential backoff
func (b BackoffConfig) CalculateDelay(attempt int) time.Duration {
	if attempt < 0 {
		return b.InitialDelay
	}

	delay := float64(b.InitialDelay) * math.Pow(b.Multiplier, float64(attempt))
	if delay > float64(b.MaxDelay) {
		delay = float64(b.MaxDelay)
	}

	// Add jitter to prevent thundering herd
	if b.Jitter {
		// Add up to 25% jitter
		jitter := delay * 0.25 * float64(time.Now().UnixNano()%1000-500) / 500
		delay += jitter
	}

	return time.Duration(delay)
}

// HealthConfig defines configuration for health monitoring
type HealthConfig struct {
	CheckInterval           time.Duration `json:"check_interval"`
	MaxRecoveryAttempts     int           `json:"max_recovery_attempts"`
	RecoveryBackoff         BackoffConfig `json:"recovery_backoff"`
	AutoRecovery            bool          `json:"auto_recovery"`
	ConnectionTimeout       time.Duration `json:"connection_timeout"`
	StuckStateTimeout       time.Duration `json:"stuck_state_timeout"`
	HealthCheckTimeout      time.Duration `json:"health_check_timeout"`
	EmitHealthEvents        bool          `json:"emit_health_events"`
	RecoveryGracePeriod     time.Duration `json:"recovery_grace_period"`
	ConnectionQualityWindow time.Duration `json:"connection_quality_window"`
}

// DefaultHealthConfig returns a sensible default health configuration
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		CheckInterval:           30 * time.Second,
		MaxRecoveryAttempts:     5,
		RecoveryBackoff:         DefaultBackoffConfig(),
		AutoRecovery:            true,
		ConnectionTimeout:       10 * time.Second,
		StuckStateTimeout:       5 * time.Minute,
		HealthCheckTimeout:      5 * time.Second,
		EmitHealthEvents:        true,
		RecoveryGracePeriod:     2 * time.Minute,
		ConnectionQualityWindow: 10 * time.Minute,
	}
}

// ServiceHealth tracks health status for a service (Discord or Mumble)
type ServiceHealth struct {
	Name                string
	Connected           bool
	LastConnected       time.Time
	LastHealthCheck     time.Time
	RecoveryAttempts    int
	LastRecoveryAttempt time.Time
	ConsecutiveFailures int
	ConnectionQuality   float64 // 0.0-1.0 based on connection stability
	LastErrors          []error // Circular buffer of recent errors
	StuckSince          time.Time
	LastHealthy         *bool   // Track last health status for change detection
}

// IsHealthy returns true if the service is considered healthy
func (sh *ServiceHealth) IsHealthy() bool {
	return sh.Connected && sh.ConsecutiveFailures == 0
}

// IsStuck returns true if the service has been in a non-healthy state for too long
func (sh *ServiceHealth) IsStuck(timeout time.Duration) bool {
	if sh.IsHealthy() {
		return false
	}
	return !sh.StuckSince.IsZero() && time.Since(sh.StuckSince) > timeout
}

// UpdateConnectionQuality updates the connection quality score based on recent performance
func (sh *ServiceHealth) UpdateConnectionQuality(windowDuration time.Duration) {
	// If recently connected, quality is high
	if sh.Connected && time.Since(sh.LastConnected) < windowDuration {
		successWindow := float64(time.Since(sh.LastConnected)) / float64(windowDuration)
		failureRatio := float64(sh.ConsecutiveFailures) / 10.0 // Scale failures
		sh.ConnectionQuality = math.Max(0.0, math.Min(1.0, (1.0-successWindow)-(failureRatio*0.2)))
		return
	}
	
	// If disconnected for a while, quality degrades
	if !sh.Connected {
		disconnectPenalty := float64(time.Since(sh.LastConnected)) / float64(windowDuration)
		sh.ConnectionQuality = math.Max(0.0, 1.0-disconnectPenalty)
		return
	}
	
	// Default quality based on failure rate
	failureRatio := float64(sh.ConsecutiveFailures) / 10.0
	sh.ConnectionQuality = math.Max(0.0, math.Min(1.0, 1.0-(failureRatio*0.1)))
}

// HealthMonitor monitors bridge health and performs automatic recovery
type HealthMonitor struct {
	bridge      *BridgeInstance
	config      HealthConfig
	
	// Service health tracking
	discordHealth *ServiceHealth
	mumbleHealth  *ServiceHealth
	
	// Overall bridge health
	bridgeStartTime     time.Time
	lastOverallCheck    time.Time
	overallHealthy      bool
	
	// Control
	ctx     context.Context
	cancel  context.CancelFunc
	mutex   sync.RWMutex
	
	// Recovery control
	recoveryInProgress map[string]bool // service name -> recovery in progress
	recoveryMutex      sync.Mutex
	
	logger Logger
}

// NewHealthMonitor creates a new health monitor
func NewHealthMonitor(bridge *BridgeInstance, config HealthConfig, logger Logger) *HealthMonitor {
	if logger == nil {
		logger = NewConsoleLogger()
	}

	return &HealthMonitor{
		bridge: bridge,
		config: config,
		discordHealth: &ServiceHealth{
			Name:        "discord",
			LastErrors:  make([]error, 0, 5),
		},
		mumbleHealth: &ServiceHealth{
			Name:        "mumble", 
			LastErrors:  make([]error, 0, 5),
		},
		recoveryInProgress: make(map[string]bool),
		logger:            logger.WithBridgeID(bridge.ID),
	}
}

// Start begins health monitoring
func (hm *HealthMonitor) Start(ctx context.Context) {
	hm.ctx, hm.cancel = context.WithCancel(ctx)
	hm.bridgeStartTime = time.Now()
	
	hm.logger.Info("HEALTH", "Starting health monitor")
	
	go hm.monitorLoop()
}

// Stop stops health monitoring
func (hm *HealthMonitor) Stop() {
	if hm.cancel != nil {
		hm.cancel()
	}
	hm.logger.Info("HEALTH", "Health monitor stopped")
}

// GetHealth returns current health status
func (hm *HealthMonitor) GetHealth() (discord, mumble ServiceHealth, overallHealthy bool) {
	hm.mutex.RLock()
	defer hm.mutex.RUnlock()
	
	return *hm.discordHealth, *hm.mumbleHealth, hm.overallHealthy
}

// UpdateConnectionStatus updates the connection status for a service
func (hm *HealthMonitor) UpdateConnectionStatus(service string, connected bool, err error) {
	hm.logger.Debug("HEALTH_UPDATE", fmt.Sprintf("Updating %s connection status: connected=%t, err=%v", service, connected, err))
	
	hm.mutex.Lock()
	defer hm.mutex.Unlock()
	
	var serviceHealth *ServiceHealth
	switch service {
	case "discord":
		serviceHealth = hm.discordHealth
	case "mumble":
		serviceHealth = hm.mumbleHealth
	default:
		hm.logger.Warn("HEALTH", fmt.Sprintf("Unknown service: %s", service))
		return
	}
	
	now := time.Now()
	oldConnected := serviceHealth.Connected
	serviceHealth.Connected = connected
	serviceHealth.LastHealthCheck = now
	
	if connected && !oldConnected {
		// Connection restored
		serviceHealth.LastConnected = now
		serviceHealth.ConsecutiveFailures = 0
		serviceHealth.StuckSince = time.Time{}
		hm.logger.Info("HEALTH", fmt.Sprintf("%s connection restored", service))
		
		// Emit connection event
		if hm.bridge.eventDispatcher != nil {
			eventType := EventDiscordConnected
			if service == "mumble" {
				eventType = EventMumbleConnected
			}
			hm.bridge.eventDispatcher.EmitEvent(eventType, map[string]interface{}{
				"service": service,
			}, nil)
		}
	} else if !connected && oldConnected {
		// Connection lost
		if serviceHealth.StuckSince.IsZero() {
			serviceHealth.StuckSince = now
		}
		hm.logger.Warn("HEALTH", fmt.Sprintf("%s connection lost", service))
		
		// Emit disconnection event
		if hm.bridge.eventDispatcher != nil {
			eventType := EventDiscordDisconnected
			if service == "mumble" {
				eventType = EventMumbleDisconnected
			}
			hm.bridge.eventDispatcher.EmitEvent(eventType, map[string]interface{}{
				"service": service,
			}, err)
		}
	}
	
	if err != nil {
		serviceHealth.ConsecutiveFailures++
		// Add error to circular buffer
		if len(serviceHealth.LastErrors) >= 5 {
			serviceHealth.LastErrors = serviceHealth.LastErrors[1:]
		}
		serviceHealth.LastErrors = append(serviceHealth.LastErrors, err)
		
		hm.logger.Debug("HEALTH", fmt.Sprintf("%s error: %v (consecutive failures: %d)", 
			service, err, serviceHealth.ConsecutiveFailures))
	}
	
	// Update connection quality
	serviceHealth.UpdateConnectionQuality(hm.config.ConnectionQualityWindow)
}

// monitorLoop runs the main health monitoring loop
func (hm *HealthMonitor) monitorLoop() {
	ticker := time.NewTicker(hm.config.CheckInterval)
	defer ticker.Stop()
	
	hm.logger.Debug("HEALTH", "Health monitoring loop started")
	
	for {
		select {
		case <-ticker.C:
			hm.performHealthCheck()
		case <-hm.ctx.Done():
			hm.logger.Debug("HEALTH", "Health monitoring loop stopped")
			return
		}
	}
}

// performHealthCheck performs a comprehensive health check
func (hm *HealthMonitor) performHealthCheck() {
	hm.logger.Debug("HEALTH", "Performing health check")
	
	now := time.Now()
	hm.mutex.Lock()
	hm.lastOverallCheck = now
	hm.mutex.Unlock()
	
	// Check individual service health
	discordHealthy := hm.checkServiceHealth("discord")
	mumbleHealthy := hm.checkServiceHealth("mumble")
	
	// Update overall health status
	hm.mutex.Lock()
	oldOverallHealthy := hm.overallHealthy
	hm.overallHealthy = discordHealthy && mumbleHealthy
	hm.mutex.Unlock()
	
	// Emit health check events
	if hm.config.EmitHealthEvents && hm.bridge.eventDispatcher != nil {
		if hm.overallHealthy {
			hm.bridge.eventDispatcher.EmitEvent(EventHealthCheckPassed, map[string]interface{}{
				"discord_healthy": discordHealthy,
				"mumble_healthy":  mumbleHealthy,
				"uptime_seconds":  time.Since(hm.bridgeStartTime).Seconds(),
			}, nil)
		} else {
			hm.bridge.eventDispatcher.EmitEvent(EventHealthCheckFailed, map[string]interface{}{
				"discord_healthy": discordHealthy,
				"mumble_healthy":  mumbleHealthy,
				"uptime_seconds":  time.Since(hm.bridgeStartTime).Seconds(),
			}, nil)
		}
	}
	
	// Log health changes
	if oldOverallHealthy != hm.overallHealthy {
		if hm.overallHealthy {
			hm.logger.Info("HEALTH", "Bridge health restored")
		} else {
			hm.logger.Warn("HEALTH", fmt.Sprintf("Bridge health degraded (Discord: %v, Mumble: %v)", 
				discordHealthy, mumbleHealthy))
		}
		// Debug log only when overall status changes
		hm.logger.Debug("HEALTH", fmt.Sprintf("Health check completed - overall: %v (Discord: %v, Mumble: %v) (CHANGED)", 
			hm.overallHealthy, discordHealthy, mumbleHealthy))
	}
}

// checkServiceHealth checks the health of a specific service and triggers recovery if needed
func (hm *HealthMonitor) checkServiceHealth(serviceName string) bool {
	hm.mutex.RLock()
	var serviceHealth *ServiceHealth
	switch serviceName {
	case "discord":
		serviceHealth = hm.discordHealth
	case "mumble":
		serviceHealth = hm.mumbleHealth
	default:
		hm.mutex.RUnlock()
		return false
	}
	
	isHealthy := serviceHealth.IsHealthy()
	isStuck := serviceHealth.IsStuck(hm.config.StuckStateTimeout)
	shouldRecover := hm.config.AutoRecovery && !isHealthy && 
		(isStuck || serviceHealth.ConsecutiveFailures > 3)
	
	// Debug logging only when health status changes
	if serviceHealth.LastHealthy == nil || *serviceHealth.LastHealthy != isHealthy {
		hm.logger.Debug("HEALTH_CHECK", fmt.Sprintf("%s health: Connected=%t, ConsecutiveFailures=%d, IsHealthy=%t (CHANGED)", 
			serviceName, serviceHealth.Connected, serviceHealth.ConsecutiveFailures, isHealthy))
		// Update the last health status
		serviceHealth.LastHealthy = &isHealthy
	}
	
	hm.mutex.RUnlock()
	
	// Attempt recovery if needed
	if shouldRecover {
		go hm.attemptRecovery(serviceName)
	}
	
	return isHealthy
}

// attemptRecovery attempts to recover a failed service
func (hm *HealthMonitor) attemptRecovery(serviceName string) {
	// Check if recovery is already in progress
	hm.recoveryMutex.Lock()
	if hm.recoveryInProgress[serviceName] {
		hm.recoveryMutex.Unlock()
		return
	}
	hm.recoveryInProgress[serviceName] = true
	hm.recoveryMutex.Unlock()
	
	defer func() {
		hm.recoveryMutex.Lock()
		hm.recoveryInProgress[serviceName] = false
		hm.recoveryMutex.Unlock()
	}()
	
	hm.mutex.Lock()
	var serviceHealth *ServiceHealth
	switch serviceName {
	case "discord":
		serviceHealth = hm.discordHealth
	case "mumble":
		serviceHealth = hm.mumbleHealth
	default:
		hm.mutex.Unlock()
		return
	}
	
	// Check if we've exceeded max recovery attempts
	if serviceHealth.RecoveryAttempts >= hm.config.MaxRecoveryAttempts {
		hm.mutex.Unlock()
		hm.logger.Error("HEALTH", fmt.Sprintf("Max recovery attempts reached for %s, giving up", serviceName))
		
		if hm.bridge.eventDispatcher != nil {
			hm.bridge.eventDispatcher.EmitEvent(EventRecoveryGaveUp, map[string]interface{}{
				"service": serviceName,
				"attempts": serviceHealth.RecoveryAttempts,
			}, fmt.Errorf("max recovery attempts reached"))
		}
		return
	}
	
	// Check grace period since last attempt
	if time.Since(serviceHealth.LastRecoveryAttempt) < hm.config.RecoveryGracePeriod {
		hm.mutex.Unlock()
		hm.logger.Debug("HEALTH", fmt.Sprintf("Recovery grace period active for %s, skipping", serviceName))
		return
	}
	
	attempt := serviceHealth.RecoveryAttempts
	serviceHealth.RecoveryAttempts++
	serviceHealth.LastRecoveryAttempt = time.Now()
	hm.mutex.Unlock()
	
	hm.logger.Info("HEALTH", fmt.Sprintf("Attempting recovery for %s (attempt %d/%d)", 
		serviceName, attempt+1, hm.config.MaxRecoveryAttempts))
	
	// Emit recovery attempt event
	if hm.bridge.eventDispatcher != nil {
		hm.bridge.eventDispatcher.EmitEvent(EventRecoveryAttempted, map[string]interface{}{
			"service": serviceName,
			"attempt": attempt + 1,
			"max_attempts": hm.config.MaxRecoveryAttempts,
		}, nil)
	}
	
	// Calculate backoff delay
	delay := hm.config.RecoveryBackoff.CalculateDelay(attempt)
	hm.logger.Debug("HEALTH", fmt.Sprintf("Waiting %v before %s recovery attempt", delay, serviceName))
	
	select {
	case <-time.After(delay):
		// Proceed with recovery
	case <-hm.ctx.Done():
		// Context cancelled, abort recovery
		return
	}
	
	// Perform the actual recovery based on service type
	var err error
	switch serviceName {
	case "discord":
		err = hm.recoverDiscord()
	case "mumble":
		err = hm.recoverMumble()
	}
	
	if err != nil {
		hm.logger.Error("HEALTH", fmt.Sprintf("Recovery failed for %s: %v", serviceName, err))
		
		if hm.bridge.eventDispatcher != nil {
			hm.bridge.eventDispatcher.EmitEvent(EventRecoveryFailed, map[string]interface{}{
				"service": serviceName,
				"attempt": attempt + 1,
			}, err)
		}
	} else {
		hm.logger.Info("HEALTH", fmt.Sprintf("Recovery succeeded for %s", serviceName))
		
		// Reset recovery attempts on success
		hm.mutex.Lock()
		serviceHealth.RecoveryAttempts = 0
		hm.mutex.Unlock()
		
		if hm.bridge.eventDispatcher != nil {
			hm.bridge.eventDispatcher.EmitEvent(EventRecoverySucceeded, map[string]interface{}{
				"service": serviceName,
				"attempt": attempt + 1,
			}, nil)
		}
	}
}

// recoverDiscord attempts to recover Discord connection
func (hm *HealthMonitor) recoverDiscord() error {
	hm.logger.Debug("HEALTH", "Attempting Discord recovery")
	
	// Access the bridge's Discord connection manager
	if hm.bridge.State == nil || hm.bridge.State.DiscordConnectionManager == nil {
		return fmt.Errorf("discord connection manager not available")
	}
	
	// Force reconnection by restarting the connection manager
	// The connection manager will handle the actual reconnection logic
	return hm.bridge.State.DiscordConnectionManager.Stop()
	// The connection manager's internal logic will restart automatically
}

// recoverMumble attempts to recover Mumble connection  
func (hm *HealthMonitor) recoverMumble() error {
	hm.logger.Debug("HEALTH", "Attempting Mumble recovery")
	
	// Access the bridge's Mumble connection manager
	if hm.bridge.State == nil || hm.bridge.State.MumbleConnectionManager == nil {
		return fmt.Errorf("mumble connection manager not available")
	}
	
	// Force reconnection by restarting the connection manager
	// The connection manager will handle the actual reconnection logic
	return hm.bridge.State.MumbleConnectionManager.Stop()
	// The connection manager's internal logic will restart automatically
}