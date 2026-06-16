import argparse
import random
import yaml
import os
import socket
import json
import time

def detectar_ativos(lider_ip):
    portas_ativas = set()
    drones_ativos = set()
    if not lider_ip:
        return portas_ativas, drones_ativos

    # Se informou lider, assume que a porta dele (6000) está ativa
    portas_ativas.add(6000)

    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(1.0)
        s.connect((lider_ip, 6000))

        msg = {
            "tipo": "REGISTRO",
            "broker_id": "MONITOR-AUTO-DISCOVER"
        }
        s.sendall((json.dumps(msg) + "\n").encode('utf-8'))

        data = b""
        start_time = time.time()
        while time.time() - start_time < 1.0:
            try:
                chunk = s.recv(4096)
                if not chunk:
                    break
                data += chunk
                lines = data.split(b"\n")
                data = lines[-1]
                for line in lines[:-1]:
                    if not line:
                        continue
                    try:
                        res = json.loads(line.decode('utf-8'))
                        if res.get("tipo") == "SINC_DRONE" and "drone" in res:
                            drone_info = res["drone"]
                            drone_id = drone_info.get("drone_id")
                            if drone_id:
                                parts = drone_id.split("_")
                                if parts:
                                    try:
                                        drones_ativos.add(int(parts[-1]))
                                    except ValueError:
                                        pass
                        elif res.get("tipo") == "PEER_LIST" and "peers" in res:
                            peers = res["peers"]
                            for peer in peers:
                                if ":" in peer:
                                    try:
                                        port = int(peer.split(":")[-1])
                                        portas_ativas.add(port)
                                    except ValueError:
                                        pass
                    except json.JSONDecodeError:
                        pass
            except socket.timeout:
                break
        s.close()
    except Exception:
        pass

    return portas_ativas, drones_ativos

def sugerir_start(mode, count, lider_ip):
    portas_ativas, drones_ativos = detectar_ativos(lider_ip)
    
    if mode == "brokers" or mode == "sensores":
        active_indices = set()
        for p in portas_ativas:
            if 6000 <= p <= 6003:
                active_indices.add(p - 6000 + 1)
        
        for i in range(1, 5):
            if i + count - 1 > 4:
                break
            if all(j not in active_indices for j in range(i, i + count)):
                return i
        for i in range(1, 5):
            if i not in active_indices:
                return i
        return 1
        
    elif mode == "drones":
        i = 1
        while True:
            if all(j not in drones_ativos for j in range(i, i + count)):
                return i
            i += 1
            if i > 100:
                break
        return i
        
    return 1

def gerar_yaml(mode, count, lider_ip, start_index=-1):
    if start_index == -1:
        start_index = sugerir_start(mode, count, lider_ip)

    services = {}

    brokers_base = [
        {"id": "B1", "setor": "Setor_Noroeste", "port": 6000},
        {"id": "B2", "setor": "Setor_Nordeste", "port": 6001},
        {"id": "B3", "setor": "Setor_Sudoeste", "port": 6002},
        {"id": "B4", "setor": "Setor_Sudeste",  "port": 6003}
    ]

    drone_sectors = [
        {"setor": "NW", "x": 250, "y": 750},
        {"setor": "NE", "x": 750, "y": 750},
        {"setor": "SW", "x": 250, "y": 250},
        {"setor": "SE", "x": 750, "y": 250}
    ]
    
    types = ["radar", "sonar", "boia", "visual", "meteo"]

    if mode == "lider":
        b = brokers_base[0] # B1
        services["broker1"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.broker"},
            "container_name": "hormuznet_broker1",
            "network_mode": "host",
            "command": [f"-id={b['id']}", f"-setor={b['setor']}", "-udp=224.1.2.3:9876", f"-tcp=0.0.0.0:{b['port']}"],
            "environment": {
                "ENABLE_TOKEN_REPLENISHMENT": "true"
            },
            "restart": "on-failure"
        }

    elif mode == "brokers":
        for i in range(start_index - 1, min(start_index - 1 + count, 4)):
            b = brokers_base[i]
            services[f"broker{i+1}"] = {
                "build": {"context": "./code", "dockerfile": "Dockerfile.broker"},
                "container_name": f"hormuznet_broker{i+1}",
                "network_mode": "host",
                "command": [f"-id={b['id']}", f"-setor={b['setor']}", "-udp=224.1.2.3:9876", f"-tcp=0.0.0.0:{b['port']}", f"-lider={lider_ip}:6000"],
                "restart": "on-failure"
            }

    elif mode == "monitor":
        services["monitor"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.monitor"},
            "container_name": "hormuznet_monitor",
            "network_mode": "host",
            "command": [f"-brokers={lider_ip}:6000", "-porta=8085", "-explorer-porta=8086"],
            "restart": "on-failure"
        }

    elif mode == "drones":
        for i in range(start_index - 1, start_index - 1 + count):
            d = drone_sectors[i % len(drone_sectors)]
            id_drone = f"Drone_{d['setor']}_{i+1}"
            services[f"drone{i+1}"] = {
                "build": {"context": "./code", "dockerfile": "Dockerfile.drone"},
                "container_name": f"hormuznet_{id_drone.lower()}",
                "network_mode": "host",
                "command": [f"-id={id_drone}", f"-brokers={lider_ip}:6000", f"-x={d['x']}", f"-y={d['y']}"],
                "restart": "on-failure"
            }

    elif mode == "sensores":
        s_count = (start_index - 1) * 2 + 1
        for i in range(start_index - 1, min(start_index - 1 + count, 4)):
            b = brokers_base[i]
            if b["id"] == "B1":
                coords = [(150, 750), (350, 850)]
            elif b["id"] == "B2":
                coords = [(650, 750), (850, 850)]
            elif b["id"] == "B3":
                coords = [(150, 250), (350, 350)]
            else:
                coords = [(650, 250), (850, 350)]

            for j in range(2):
                t = random.choice(types)
                x, y = coords[j]
                id_sensor = f"{t}_{b['setor'].split('_')[1].lower()}_{s_count}"
                
                # Compute prioritized REST API port list
                broker_num = int(b["id"][1])
                primary_api_port = 7000 + (broker_num - 1)
                apis = [f"http://localhost:{primary_api_port}"]
                if lider_ip and lider_ip != "127.0.0.1":
                    apis.append(f"http://{lider_ip}:7000")
                for p in [7000, 7001, 7002, 7003]:
                    if p != primary_api_port:
                        apis.append(f"http://localhost:{p}")
                broker_api_str = ",".join(apis)

                services[f"sensor_{s_count}"] = {
                    "build": {"context": "./code", "dockerfile": "Dockerfile.sensor"},
                    "container_name": f"hormuznet_{id_sensor}",
                    "network_mode": "host",
                    "command": [
                        f"-id={id_sensor}",
                        f"-tipo={t}",
                        f"-setor={b['setor']}",
                        "-broker=224.1.2.3:9876",
                        "-intervalo=20000",
                        f"-x={x}",
                        f"-y={y}",
                        f"-broker-api={broker_api_str}"
                    ],
                    "restart": "on-failure"
                }
                s_count += 1

    compose_dict = {
        "version": "3.8",
        "services": services
    }

    with open("docker-compose-temp.yml", "w") as f:
        yaml.dump(compose_dict, f, sort_keys=False, default_flow_style=False)

if __name__ == "__main__":
    parser = argparse.ArgumentParser(description="Gera docker-compose dinâmico para HormuzNet")
    parser.add_argument("--mode", choices=["lider", "brokers", "monitor", "drones", "sensores"], required=True)
    parser.add_argument("--count", type=int, default=1)
    parser.add_argument("--lider", type=str, default="")
    parser.add_argument("--start", type=int, default=-1, help="ID/Indice de inicio da numeracao (-1 para autodetectar)")
    args = parser.parse_args()

    gerar_yaml(args.mode, args.count, args.lider, args.start)
