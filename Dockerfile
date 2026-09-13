FROM node:22-alpine AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.25-alpine AS go-build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . ./
COPY --from=web-build /src/web/dist ./web/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /atoms-app ./cmd/atoms-app

FROM alpine:3.22
COPY --from=go-build /atoms-app /usr/local/bin/atoms-app
COPY --from=web-build /src/web/dist /app/web/dist
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/atoms-app"]
