# Start from official Go image
FROM golang:1.24 AS builder

# Set working directory inside the container
WORKDIR /app

# Copy go.mod and go.sum first (caching)
COPY go.mod go.sum ./

# Download dependencies
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o redirection-operator main.go

# Minimal runtime image
FROM gcr.io/distroless/base-debian10
WORKDIR /app
COPY --from=builder /app/redirection-operator .
CMD ["./redirection-operator"]