package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
	"github.com/rs/cors"
)

// LocationPayload es la estructura de datos de ubicación enviada por el transportista
type LocationPayload struct {
	GroupID   string  `json:"groupId"`
	UserID    string  `json:"userId"`
	UserName  string  `json:"userName"`
	Lat       float64 `json:"lat"`
	Lng       float64 `json:"lng"`
	Status    string  `json:"status"`
	Timestamp int64   `json:"timestamp"`
	Type      string  `json:"type"` // "location" | "status_change"
}

// Client representa una conexión WebSocket receptora
type Client struct {
	conn   *websocket.Conn
	send   chan []byte
	userID string
}

// Group es el canal de un grupo de transporte
type Group struct {
	id        string
	receivers map[*Client]bool
	mu        sync.RWMutex
}

// Hub administra todos los grupos activos
type Hub struct {
	groups map[string]*Group
	mu     sync.RWMutex
}

var hub = &Hub{
	groups: make(map[string]*Group),
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (h *Hub) getOrCreateGroup(id string) *Group {
	h.mu.Lock()
	defer h.mu.Unlock()
	if g, ok := h.groups[id]; ok {
		return g
	}
	g := &Group{
		id:        id,
		receivers: make(map[*Client]bool),
	}
	h.groups[id] = g
	log.Printf("[HUB] Grupo '%s' creado", id)
	return g
}

func (g *Group) addReceiver(c *Client) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.receivers[c] = true
	log.Printf("[GRUPO %s] Receptor agregado. Total: %d", g.id, len(g.receivers))
}

func (g *Group) removeReceiver(c *Client) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.receivers, c)
	log.Printf("[GRUPO %s] Receptor eliminado. Total: %d", g.id, len(g.receivers))
}

func (g *Group) broadcast(data []byte) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	for client := range g.receivers {
		select {
		case client.send <- data:
		default:
			// Canal lleno, cerrar cliente lento
			close(client.send)
			delete(g.receivers, client)
		}
	}
}

// writePump escribe mensajes al WebSocket del cliente receptor
func (c *Client) writePump(group *Group) {
	ticker := time.NewTicker(30 * time.Second)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				return
			}
		case <-ticker.C:
			c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// sendHandler — endpoint para el TRANSPORTISTA que envía su ubicación
// WS: /ws/send/{group}
func sendHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	groupID := vars["group"]

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[SEND] Error upgrade: %v", err)
		return
	}
	defer conn.Close()

	group := hub.getOrCreateGroup(groupID)
	log.Printf("[SEND] Transportista conectado al grupo '%s'", groupID)

	conn.SetReadLimit(4096)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("[SEND] Error lectura grupo '%s': %v", groupID, err)
			}
			break
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))

		// Enriquecer con timestamp si no viene
		var payload LocationPayload
		if jsonErr := json.Unmarshal(msg, &payload); jsonErr == nil {
			if payload.Timestamp == 0 {
				payload.Timestamp = time.Now().UnixMilli()
				if enriched, err := json.Marshal(payload); err == nil {
					msg = enriched
				}
			}
		}

		group.broadcast(msg)
	}

	log.Printf("[SEND] Transportista desconectado del grupo '%s'", groupID)
}

// receiveHandler — endpoint para TRABAJADORES/ADMINS que reciben ubicación
// WS: /ws/receive/{group}
func receiveHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	groupID := vars["group"]

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[RECEIVE] Error upgrade: %v", err)
		return
	}

	client := &Client{
		conn:   conn,
		send:   make(chan []byte, 256),
		userID: r.URL.Query().Get("userId"),
	}

	group := hub.getOrCreateGroup(groupID)
	group.addReceiver(client)

	log.Printf("[RECEIVE] Receptor '%s' conectado al grupo '%s'", client.userID, groupID)

	// Escribir en goroutine separada
	go client.writePump(group)

	// Leer para mantener viva la conexión y detectar desconexión
	conn.SetReadLimit(512)
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	}

	group.removeReceiver(client)
	log.Printf("[RECEIVE] Receptor '%s' desconectado del grupo '%s'", client.userID, groupID)
}

// statusHandler — GET para ver grupos activos (debug/admin)
func statusHandler(w http.ResponseWriter, r *http.Request) {
	hub.mu.RLock()
	defer hub.mu.RUnlock()

	type GroupInfo struct {
		ID        string `json:"id"`
		Receivers int    `json:"receivers"`
	}

	var groups []GroupInfo
	for id, g := range hub.groups {
		g.mu.RLock()
		groups = append(groups, GroupInfo{ID: id, Receivers: len(g.receivers)})
		g.mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"groups": groups,
		"total":  len(groups),
	})
}

func main() {
	r := mux.NewRouter()

	// WebSocket endpoints
	r.HandleFunc("/ws/send/{group}", sendHandler)
	r.HandleFunc("/ws/receive/{group}", receiveHandler)

	// HTTP endpoint de estado
	r.HandleFunc("/status", statusHandler).Methods("GET")
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "websocket"})
	}).Methods("GET")

	// CORS
	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	})

	handler := c.Handler(r)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	log.Println("╔════════════════════════════════════════╗")
	log.Printf("║   Servicio WebSocket corriendo en :%s ║\n", port)
	log.Println("║   /ws/send/{group}   → Transportista   ║")
	log.Println("║   /ws/receive/{group}→ Trabajadores    ║")
	log.Println("╚════════════════════════════════════════╝")

	if err := http.ListenAndServe(":"+port, handler); err != nil {
		log.Fatal("Error servidor:", err)
	}
}
