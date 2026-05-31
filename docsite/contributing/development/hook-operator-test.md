# Hook Operator Test Client

Run the local test client:

```sh
make -C omni setup-test-infra
```

The target loads:

```text
svc/hook-operator/testclient/.env
```

Default address:

```text
127.0.0.1:18080
```

Register hook entries against these callback URLs:

```text
http://127.0.0.1:18080/hooks/default
http://127.0.0.1:18080/hooks/pre-tool-use
http://127.0.0.1:18080/hooks/post-tool-use
http://127.0.0.1:18080/hooks/notification
http://127.0.0.1:18080/hooks/stop
```

The client logs every received payload and stores the latest request per hook name in memory. Inspect captured requests:

```text
http://127.0.0.1:18080/requests
```

The hook response is always:

```json
{
  "continue": true,
  "suppress_output": false
}
```
