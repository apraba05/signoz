FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -o /demo-app .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /demo-app /demo-app
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/demo-app"]
