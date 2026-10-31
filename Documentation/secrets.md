## Adding a Secret

```
gh auth token | hsh secret add --value-stdin \
  --host api.github.com --header Authorization \
  --prefix Bearer github.com
```
