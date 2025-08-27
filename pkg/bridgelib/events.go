package bridgelib

import (
	"sync"
	"time"
)

// BridgeEventType represents different types of events that can be emitted by the bridge
type BridgeEventType int

const (
	// Connection events
	EventDiscordConnecting BridgeEventType = iota
	EventDiscordConnected
	EventDiscordDisconnected
	EventDiscordReconnecting
	EventDiscordConnectionFailed

	EventMumbleConnecting
	EventMumbleConnected
	EventMumbleDisconnected
	EventMumbleReconnecting
	EventMumbleConnectionFailed

	// Bridge lifecycle events
	EventBridgeStarted
	EventBridgeStopped
	EventBridgeStarting
	EventBridgeStopping
	EventBridgeError

	// User events
	EventUserJoinedDiscord
	EventUserLeftDiscord
	EventUserJoinedMumble
	EventUserLeftMumble

	// Health and monitoring events
	EventHealthCheckPassed
	EventHealthCheckFailed
	EventRecoveryAttempted
	EventRecoverySucceeded
	EventRecoveryFailed
	EventRecoveryGaveUp

	// Configuration events
	EventConfigChanged
	EventConfigUpdateRequired

	// Audio events
	EventAudioStreamStarted
	EventAudioStreamStopped
	EventAudioQualityChanged

	// Metrics events
	EventMetricsUpdated
)

// String returns a string representation of the event type
func (e BridgeEventType) String() string {
	switch e {
	case EventDiscordConnecting:
		return "DiscordConnecting"
	case EventDiscordConnected:
		return "DiscordConnected"
	case EventDiscordDisconnected:
		return "DiscordDisconnected"
	case EventDiscordReconnecting:
		return "DiscordReconnecting"
	case EventDiscordConnectionFailed:
		return "DiscordConnectionFailed"
	case EventMumbleConnecting:
		return "MumbleConnecting"
	case EventMumbleConnected:
		return "MumbleConnected"
	case EventMumbleDisconnected:
		return "MumbleDisconnected"
	case EventMumbleReconnecting:
		return "MumbleReconnecting"
	case EventMumbleConnectionFailed:
		return "MumbleConnectionFailed"
	case EventBridgeStarted:
		return "BridgeStarted"
	case EventBridgeStopped:
		return "BridgeStopped"
	case EventBridgeStarting:
		return "BridgeStarting"
	case EventBridgeStopping:
		return "BridgeStopping"
	case EventBridgeError:
		return "BridgeError"
	case EventUserJoinedDiscord:
		return "UserJoinedDiscord"
	case EventUserLeftDiscord:
		return "UserLeftDiscord"
	case EventUserJoinedMumble:
		return "UserJoinedMumble"
	case EventUserLeftMumble:
		return "UserLeftMumble"
	case EventHealthCheckPassed:
		return "HealthCheckPassed"
	case EventHealthCheckFailed:
		return "HealthCheckFailed"
	case EventRecoveryAttempted:
		return "RecoveryAttempted"
	case EventRecoverySucceeded:
		return "RecoverySucceeded"
	case EventRecoveryFailed:
		return "RecoveryFailed"
	case EventRecoveryGaveUp:
		return "RecoveryGaveUp"
	case EventConfigChanged:
		return "ConfigChanged"
	case EventConfigUpdateRequired:
		return "ConfigUpdateRequired"
	case EventAudioStreamStarted:
		return "AudioStreamStarted"
	case EventAudioStreamStopped:
		return "AudioStreamStopped"
	case EventAudioQualityChanged:
		return "AudioQualityChanged"
	case EventMetricsUpdated:
		return "MetricsUpdated"
	default:
		return "Unknown"
	}
}

// BridgeEvent represents an event that occurred in the bridge
type BridgeEvent struct {
	Type      BridgeEventType            `json:"type"`
	Timestamp time.Time                  `json:"timestamp"`
	BridgeID  string                     `json:"bridge_id"`
	Data      map[string]interface{}     `json:"data"`
	Error     error                      `json:"error,omitempty"`
}

// BridgeEventHandler defines the signature for event handlers
type BridgeEventHandler func(event BridgeEvent)

// EventDispatcher manages event handlers and dispatches events
type EventDispatcher struct {
	bridgeID       string
	eventHandlers  map[BridgeEventType][]BridgeEventHandler
	globalHandlers []BridgeEventHandler
	mutex          sync.RWMutex
	bufferSize     int
	eventChan      chan BridgeEvent
	stopChan       chan struct{}
}

// NewEventDispatcher creates a new event dispatcher
func NewEventDispatcher(bridgeID string, bufferSize int) *EventDispatcher {
	if bufferSize <= 0 {
		bufferSize = 100 // Default buffer size
	}

	return &EventDispatcher{
		bridgeID:      bridgeID,
		eventHandlers: make(map[BridgeEventType][]BridgeEventHandler),
		bufferSize:    bufferSize,
		eventChan:     make(chan BridgeEvent, bufferSize),
		stopChan:      make(chan struct{}),
	}
}

// Start begins processing events
func (ed *EventDispatcher) Start() {
	go ed.processEvents()
}

// Stop stops the event dispatcher
func (ed *EventDispatcher) Stop() {
	close(ed.stopChan)
	// Drain any remaining events
	for {
		select {
		case <-ed.eventChan:
			// Drain event
		default:
			return
		}
	}
}

// RegisterHandler registers a handler for a specific event type
func (ed *EventDispatcher) RegisterHandler(eventType BridgeEventType, handler BridgeEventHandler) {
	ed.mutex.Lock()
	defer ed.mutex.Unlock()

	if ed.eventHandlers[eventType] == nil {
		ed.eventHandlers[eventType] = make([]BridgeEventHandler, 0)
	}
	ed.eventHandlers[eventType] = append(ed.eventHandlers[eventType], handler)
}

// UnregisterHandler removes a handler for a specific event type
func (ed *EventDispatcher) UnregisterHandler(eventType BridgeEventType, handler BridgeEventHandler) {
	ed.mutex.Lock()
	defer ed.mutex.Unlock()

	handlers := ed.eventHandlers[eventType]
	for i, h := range handlers {
		// Compare function pointers (note: this may not work perfectly in all cases)
		if &h == &handler {
			ed.eventHandlers[eventType] = append(handlers[:i], handlers[i+1:]...)
			break
		}
	}
}

// RegisterGlobalHandler registers a handler that receives all events
func (ed *EventDispatcher) RegisterGlobalHandler(handler BridgeEventHandler) {
	ed.mutex.Lock()
	defer ed.mutex.Unlock()

	ed.globalHandlers = append(ed.globalHandlers, handler)
}

// UnregisterGlobalHandler removes a global handler
func (ed *EventDispatcher) UnregisterGlobalHandler(handler BridgeEventHandler) {
	ed.mutex.Lock()
	defer ed.mutex.Unlock()

	for i, h := range ed.globalHandlers {
		if &h == &handler {
			ed.globalHandlers = append(ed.globalHandlers[:i], ed.globalHandlers[i+1:]...)
			break
		}
	}
}

// EmitEvent emits an event (non-blocking)
func (ed *EventDispatcher) EmitEvent(eventType BridgeEventType, data map[string]interface{}, err error) {
	event := BridgeEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		BridgeID:  ed.bridgeID,
		Data:      data,
		Error:     err,
	}

	select {
	case ed.eventChan <- event:
		// Event queued successfully
	default:
		// Event buffer full, drop event (could add logging here)
	}
}

// EmitEventSync emits an event synchronously (blocking until handlers complete)
func (ed *EventDispatcher) EmitEventSync(eventType BridgeEventType, data map[string]interface{}, err error) {
	event := BridgeEvent{
		Type:      eventType,
		Timestamp: time.Now(),
		BridgeID:  ed.bridgeID,
		Data:      data,
		Error:     err,
	}

	ed.dispatchEvent(event)
}

// processEvents processes events from the event channel
func (ed *EventDispatcher) processEvents() {
	for {
		select {
		case event := <-ed.eventChan:
			ed.dispatchEvent(event)
		case <-ed.stopChan:
			return
		}
	}
}

// dispatchEvent dispatches an event to all registered handlers
func (ed *EventDispatcher) dispatchEvent(event BridgeEvent) {
	ed.mutex.RLock()
	defer ed.mutex.RUnlock()

	// Call specific handlers for this event type
	if handlers, exists := ed.eventHandlers[event.Type]; exists {
		for _, handler := range handlers {
			// Call handler in a goroutine to prevent blocking
			go func(h BridgeEventHandler) {
				defer func() {
					if r := recover(); r != nil {
						// Handler panicked, but don't let it crash the dispatcher
					}
				}()
				h(event)
			}(handler)
		}
	}

	// Call global handlers
	for _, handler := range ed.globalHandlers {
		go func(h BridgeEventHandler) {
			defer func() {
				if r := recover(); r != nil {
					// Handler panicked, but don't let it crash the dispatcher
				}
			}()
			h(event)
		}(handler)
	}
}

// GetHandlerCount returns the number of handlers registered for an event type
func (ed *EventDispatcher) GetHandlerCount(eventType BridgeEventType) int {
	ed.mutex.RLock()
	defer ed.mutex.RUnlock()

	if handlers, exists := ed.eventHandlers[eventType]; exists {
		return len(handlers)
	}
	return 0
}

// GetGlobalHandlerCount returns the number of global handlers
func (ed *EventDispatcher) GetGlobalHandlerCount() int {
	ed.mutex.RLock()
	defer ed.mutex.RUnlock()

	return len(ed.globalHandlers)
}