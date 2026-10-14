# tailweave

Ein Rechner ist gleichzeitig echter Node in mehreren Tailnets, mit TUN, UDP und
MagicDNS, ohne Proxy. Konflikte zwischen den Tailnets werden erkannt, angezeigt
und lassen sich per Klick lösen.

## Warum

Tailscale kann pro Gerät nur ein Tailnet gleichzeitig. Proxy-Lösungen wie TailMux
leiten nur TCP weiter. WebRTC (z. B. JetKVM), QUIC und Programme ohne Proxy-Support
funktionieren damit nicht.

## Wie es funktioniert

- **Eine Tailscale-Instanz pro Tailnet**, jede isoliert mit eigenem Netzwerk-Stack
  (Linux: Network Namespace, macOS: Backend in einer Network Extension).
- **Ein Haupt-Tailnet** behält seine echten IPs. Alle weiteren Tailnets werden auf
  eigene IP-Bereiche umgesetzt, damit sich gleiche `100.x`-Adressen nicht stören.
- **Eigener DNS-Dienst**, der Anfragen nach Domain auf das richtige Tailnet
  verteilt und die umgesetzten IPs zurückgibt.
- **Daemon mit Oberfläche:** Tailnets hinzufügen und einloggen, Status und Peers
  ansehen, Konflikte anzeigen und lösen.

## Konflikte, die erkannt werden

| Konflikt | Lösungsoptionen |
| --- | --- |
| Gleiche IPs in mehreren Tailnets | Tailnet umsetzen, Haupt-Tailnet wechseln, Konfig-Vorschlag für den Admin |
| Überlappende Subnet-Routes | Route ignorieren oder umsetzen |
| Subnet-Route = eigenes LAN | Route ignorieren oder umsetzen |
| MagicDNS (`100.100.100.100`) | Eigener DNS-Dienst übernimmt |
| Mehrere Exit-Nodes | Nur einen erlauben |
| Gleiche DNS-Suffixe | Priorität festlegen |

## Vorgehen

1. Recherche: Gibt es das schon, oder plant Tailscale es?
2. Prototyp unter Linux mit zwei Test-Headscales mit absichtlich gleichen IPs
3. macOS-Version als Network Extension
