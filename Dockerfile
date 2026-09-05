FROM golang:1.25 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY pkg ./pkg
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/crossplane-plan ./cmd/crossplane-plan

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/crossplane-plan /crossplane-plan

USER nonroot:nonroot
ENTRYPOINT ["/crossplane-plan"]
