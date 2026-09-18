```bash
go run ./cmd/compare
```

```
stock    views=2  . other
patched  views=1  .
PASS  2 Views -> 1 View
```

```bash
GOPLS_STOCK=gopls GOPLS_PATCHED=/path/to/patched go run ./cmd/loadtest
```

```
8 nested   9 views 1525 MB -> 1 view 218 MB   86%
16 nested  17 views 2467 MB -> 1 view 223 MB  91%
```
