# pion test program


## Compile

```
go build
```

## Example session

First instance

```
./wrtcion -l localhost:8001
```

Second instance

```
./wrtcion -l localhost:8002
```

In the first instance's "chat" window, enter:

```
/call localhost:8002
```

The audio should play from the second instance using gstreamer.

## Problems

Audio doesn't sound right. It sounds as if some samples or packets are
skipped/missing.
