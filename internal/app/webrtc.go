package app

import (
	"log/slog"
	"net"
	"os"
	"strings"

	"github.com/pion/webrtc/v4"
)

func detectPublicIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return a.IP.String()
	}
	return ""
}

func buildBrowserAPI(udpPort int, externalIPs []string) (*webrtc.API, error) {
	if udpPort <= 0 {
		return webrtc.NewAPI(), nil
	}

	se := webrtc.SettingEngine{}
	
	// Adiciona suporte a NAT 1:1 se houver IPs externos configurados
	if len(externalIPs) > 0 {
		publicIP := externalIPs[0]
		if publicIP == "auto" {
			publicIP = detectPublicIP()
		}
		if publicIP != "" {
			se.SetNAT1To1IPs([]string{publicIP}, webrtc.ICECandidateTypeHost)
			slog.Info("webrtc media engine: NAT 1:1 public IP enabled", "ip", publicIP)
		}
	}

	// Força os tipos de rede aceitos pelo Pion
	se.SetNetworkTypes([]webrtc.NetworkType{
		webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6,
		webrtc.NetworkTypeTCP4, webrtc.NetworkTypeTCP6,
	})

	// Multiplexador UDP em porta fixa única
	udpConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: udpPort})
	if err != nil {
		slog.Error("webrtc media engine: failed to bind UDP mux port, falling back to ephemeral", "port", udpPort, "err", err)
		return webrtc.NewAPI(), nil
	}
	se.SetICEUDPMux(webrtc.NewICEUDPMux(nil, udpConn))
	slog.Info("webrtc media engine: UDP Mux enabled", "port", udpPort)

	// Multiplexador TCP na mesma porta para fallback (ICE-TCP)
	if tcpListener, terr := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4zero, Port: udpPort}); terr == nil {
		se.SetICETCPMux(webrtc.NewICETCPMux(nil, tcpListener, 8))
		slog.Info("webrtc media engine: ICE-TCP fallback enabled", "port", udpPort)
	} else {
		slog.Warn("webrtc media engine: ICE-TCP bind failed (ports already in use or restricted)", "port", udpPort, "err", terr)
	}

	return webrtc.NewAPI(webrtc.WithSettingEngine(se)), nil
}

func defaultRouteInterface() string {
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return ""
	}
	return parseDefaultRoute(string(data))
}

func parseDefaultRoute(table string) string {
	for _, line := range strings.Split(table, "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[1] == "00000000" && fields[0] != "" {
			return fields[0]
		}
	}
	return ""
}
