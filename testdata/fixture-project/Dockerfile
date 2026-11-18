# The fixture project's image, for hosting it (section 19) in a later milestone. Pinned by digest,
# as everything in Crew's repository is.
FROM golang:1.27.3-trixie@sha256:604106102d33b1084e37ca08adf98ac0813624ae8595bd0d7f3965ecebded09c AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /fixture .

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
COPY --from=build /fixture /fixture
EXPOSE 8080
ENTRYPOINT ["/fixture"]
