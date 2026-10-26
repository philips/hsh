Sessions should exist in a workspace which is a shared filesystem with the host machine. In general this is going to be a folder with one or more git checkouts for the agent to use.

```
mkdir example-workspace
git clone https://github.com/philips/ghar
hsh run ghcr.io/philips/hsh-pi
a0e521513916
bash-5.3#
```

Then when a user wants to monitor and approve/deny proxy requests they can do that from another terminal

```
cd example-workspace
hsh proxy-watch
```

Alternatively the user can use the session id

```
cd
hsh proxy-watch --session a0e521513916
```

This may require copying the session layout from pi that is indexed by directory:

```
clanker $ ls  ~/.pi/agent/sessions/ | head -n 5
--home-clanker--
--home-clanker-apt-register--
--home-clanker-as-bid-sheets--
--home-clanker-DhwaniControl--
--home-clanker-DictateKeyboard--
```
