FROM golang:1.23-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /platform-deployer ./cmd/platform-deployer

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /platform-deployer /platform-deployer
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/platform-deployer"]
CMD ["serve", "--addr", ":8080"]
