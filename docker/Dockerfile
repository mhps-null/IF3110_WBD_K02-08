# Multi-stage Dockerfile for Milestone 2. SPA and Go API/Seed/Worker.
FROM node:20-alpine AS frontend-build
WORKDIR /src/frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM golang:1.24-alpine AS backend-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
COPY --from=frontend-build /src/frontend/dist ./frontend/dist
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -o /out/seed ./cmd/seed && \
    if [ -d ./cmd/worker ]; then CGO_ENABLED=0 go build -o /out/worker ./cmd/worker; fi

FROM alpine:3.20
WORKDIR /app
RUN apk add --no-cache ca-certificates tzdata
COPY --from=backend-build /out/ /app/bin/
COPY --from=backend-build /src/frontend/dist /app/frontend/dist
COPY migrations_meta /app/migrations_meta
ENV SPA_DIST_DIR=/app/frontend/dist
EXPOSE 8080 9091
CMD ["/app/bin/server"]
