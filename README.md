# Servicio WebSocket — Tracking en Tiempo Real

## Tecnología
- **Go 1.21+**
- gorilla/websocket
- gorilla/mux
- rs/cors

## Instalación y ejecución

```bash
# Instalar dependencias
go mod tidy

# Correr el servicio
go run main.go

# (Opcional) Compilar binario
go build -o transporte-ws main.go
./transporte-ws
```

## Puerto: 8080

## Endpoints WebSocket

| Endpoint | Uso | Quién conecta |
|---|---|---|
| `ws://localhost:8080/ws/send/{group}` | Enviar ubicación | Transportista |
| `ws://localhost:8080/ws/receive/{group}` | Recibir ubicación | Trabajadores / Admin |

### Parámetros

- `{group}`: nombre del grupo/ruta (ej: `ruta_3D`, `ruta_norte`)
- Query param en receive: `?userId=xxx`

## Formato del mensaje (JSON)

```json
{
  "groupId": "ruta_3D",
  "userId": "abc123",
  "userName": "Carlos Transportista",
  "lat": 19.432608,
  "lng": -99.133209,
  "status": "todo_en_orden",
  "timestamp": 1710000000000,
  "type": "location"
}
```

### Valores de `status`
- `en_ruta` — En ruta normal
- `todo_en_orden` — Todo en orden
- `trafico_pesado` — Tráfico pesado
- `fallo_transporte` — Fallo del transporte
- `choque` — Choque

### Valores de `type`
- `location` — Actualización de ubicación
- `status_change` — Cambio de status del transporte

## Endpoint HTTP

- `GET /health` — Estado del servicio
- `GET /status` — Grupos activos y receptores conectados
