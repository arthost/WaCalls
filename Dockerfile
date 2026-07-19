# syntax=docker/dockerfile:1
# =============================================================================
# DuoCRM Calls — Custom Image
# Mesmo padrão das outras imagens da stack: um único `docker build` gera tudo.
# =============================================================================

# ---------------------------------------------------------------------------
# Stage 1: Build do React (client)
# ---------------------------------------------------------------------------
FROM node:22-alpine AS client-builder

WORKDIR /client

COPY client/package.json client/package-lock.json ./
RUN npm ci --prefer-offline

COPY client/ ./
RUN npm run build
# Resultado: /client/dist/

# ---------------------------------------------------------------------------
# Stage 2: Build do Go (servidor)
# ---------------------------------------------------------------------------
FROM golang:alpine AS go-builder

WORKDIR /app

# Dependências de build para CGO (opus-dev)
RUN apk add --no-cache gcc musl-dev pkgconf opus-dev

COPY go.mod go.sum ./
RUN go mod download

# Copiamos o código fonte do Go
COPY . .

# Copiamos os arquivos compilados para o diretório correto esperado pelo go:embed (internal/app/webui/dist)
COPY --from=client-builder /client/dist /app/internal/app/webui/dist

# Agora compilamos com os arquivos embarcados
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /wacalls ./cmd/server

# ---------------------------------------------------------------------------
# Stage 3: Runtime mínimo
# ---------------------------------------------------------------------------
FROM alpine:3.20

# Codec opus para runtime do pion WebRTC + wget para healthcheck
RUN apk add --no-cache ca-certificates opus wget

# Copia o binário do go-builder (já contendo os arquivos frontend embutidos via embed)
COPY --from=go-builder /wacalls /usr/local/bin/wacalls

WORKDIR /app

EXPOSE 8090

HEALTHCHECK --interval=15s --timeout=5s --start-period=25s --retries=5 \
  CMD wget --no-verbose --tries=1 --spider http://localhost:8090/healthz || exit 1

CMD ["/usr/local/bin/wacalls", "--addr", ":8090", "--db", "/data/wacalls.db"]
