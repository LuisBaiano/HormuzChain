import random
import yaml

def gerar_yaml_escala():
    services = {}

    brokers_base = [
        {"id": "B1", "setor": "Setor_Noroeste", "port": 6000},
        {"id": "B2", "setor": "Setor_Nordeste", "port": 6001},
        {"id": "B3", "setor": "Setor_Sudoeste", "port": 6002},
        {"id": "B4", "setor": "Setor_Sudeste",  "port": 6003}
    ]

    # 1. Lider (B1)
    b = brokers_base[0]
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

    # 2. Seguidores (B2-B4)
    for i in range(1, 4):
        b = brokers_base[i]
        services[f"broker{i+1}"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.broker"},
            "container_name": f"hormuznet_broker{i+1}",
            "network_mode": "host",
            "command": [f"-id={b['id']}", f"-setor={b['setor']}", "-udp=224.1.2.3:9876", f"-tcp=0.0.0.0:{b['port']}", "-lider=127.0.0.1:6000"],
            "restart": "on-failure"
        }

    # 3. Monitor
    services["monitor"] = {
        "build": {"context": "./code", "dockerfile": "Dockerfile.monitor"},
        "container_name": "hormuznet_monitor",
        "network_mode": "host",
        "command": ["-brokers=127.0.0.1:6000", "-porta=8085", "-explorer-porta=8086"],
        "restart": "on-failure"
    }

    # 4. 3 Drones requested
    drone_configs = [
        {"id": "Drone_NW_1", "x": 250, "y": 750, "primary_port": 6000},
        {"id": "Drone_NE_1", "x": 750, "y": 750, "primary_port": 6001},
        {"id": "Drone_SW_1", "x": 250, "y": 250, "primary_port": 6002}
    ]
    all_broker_ports = [6000, 6001, 6002, 6003]
    for idx, d in enumerate(drone_configs):
        primary = d["primary_port"]
        ordered = [primary] + [p for p in all_broker_ports if p != primary]
        brokers_str = ",".join(f"127.0.0.1:{p}" for p in ordered)
        services[f"drone{idx+1}"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.drone"},
            "container_name": f"hormuznet_{d['id'].lower()}",
            "network_mode": "host",
            "command": [f"-id={d['id']}", f"-brokers={brokers_str}", f"-x={d['x']}", f"-y={d['y']}"],
            "restart": "on-failure"
        }

    # 5. 4 Sensores (1 por setor)
    sensor_types = ["sonar", "radar", "boia", "visual"]
    for idx, b in enumerate(brokers_base):
        setor_short = b["setor"].split("_")[1].lower()
        t = sensor_types[idx % len(sensor_types)]
        
        if b["id"] == "B1":
            x, y = 150, 750
        elif b["id"] == "B2":
            x, y = 650, 750
        elif b["id"] == "B3":
            x, y = 150, 250
        else:
            x, y = 650, 250

        id_sensor = f"{t}_{setor_short}_1"
        services[f"sensor_{idx+1}"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.sensor"},
            "container_name": f"hormuznet_{id_sensor}",
            "network_mode": "host",
            "command": [f"-id={id_sensor}", f"-tipo={t}", f"-setor={b['setor']}", "-broker=224.1.2.3:9876", "-intervalo=15000", f"-x={x}", f"-y={y}"],
            "restart": "on-failure"
        }

    # 6. 5 Empresas com 2 Navios cada (total 10 navios)
    # Endereços e chaves privadas determinísticas mapeadas no blockchain
    empresas = {
        "Maersk": {
            "addr": "0x280f33adb69caa3e5c8c",
            "priv": "9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4"
        },
        "MSC": {
            "addr": "0x2a9621c924cf329f550a",
            "priv": "cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20"
        },
        "CMA_CGM": {
            "addr": "0x7daccdb0e3eb3ce3d768",
            "priv": "682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7"
        },
        "Hapag_Lloyd": {
            "addr": "0xf7d808577df8b4454e18",
            "priv": "303b1fa143cff773c69665a3891971bde28806c59ea2d4930f9fb8ac2c861a0a"
        },
        "ONE": {
            "addr": "0x371273902bfb4590c1d5",
            "priv": "2192e8955d5e1ad1651f2f0c637e6f1ac82855747a5f42f978db28669595dc21"
        }
    }

    # Distribuição dos navios pelos setores
    vessel_coords = [
        # Noroeste
        {"x": 120, "y": 780, "api": "http://localhost:7000"},
        {"x": 220, "y": 820, "api": "http://localhost:7000"},
        # Nordeste
        {"x": 620, "y": 780, "api": "http://localhost:7001"},
        {"x": 720, "y": 820, "api": "http://localhost:7001"},
        # Sudoeste
        {"x": 120, "y": 280, "api": "http://localhost:7002"},
        {"x": 220, "y": 320, "api": "http://localhost:7002"},
        # Sudeste
        {"x": 620, "y": 280, "api": "http://localhost:7003"},
        {"x": 720, "y": 320, "api": "http://localhost:7003"},
        # Centro / Misto
        {"x": 450, "y": 500, "api": "http://localhost:7000"},
        {"x": 550, "y": 500, "api": "http://localhost:7001"}
    ]

    vessel_idx = 0
    all_api_ports = [7000, 7001, 7002, 7003]
    for comp_name, comp_info in empresas.items():
        for ship_num in [1, 2]:
            v_id = f"vessel_{comp_name.lower()}_0{ship_num}"
            coord = vessel_coords[vessel_idx]
            
            primary_port = int(coord["api"].split(":")[-1])
            ordered_api_ports = [primary_port] + [p for p in all_api_ports if p != primary_port]
            api_fallback_str = ",".join(f"http://localhost:{p}" for p in ordered_api_ports)
            
            services[f"vessel_{vessel_idx+1}"] = {
                "build": {"context": "./code", "dockerfile": "Dockerfile.vessel"},
                "container_name": f"hormuzchain_{v_id}",
                "network_mode": "host",
                "environment": {
                    "VESSEL_ID": v_id,
                    "COMPANY_NAME": comp_name,
                    "COMPANY_ADDR": comp_info["addr"],
                    "COMPANY_PRIV_KEY": comp_info["priv"],
                    "BROKER_API": api_fallback_str,
                    "X": str(coord["x"]),
                    "Y": str(coord["y"])
                },
                "restart": "on-failure"
            }
            vessel_idx += 1

    compose_dict = {
        "version": "3.8",
        "services": services
    }

    with open("docker-compose-escala.yml", "w") as f:
        yaml.dump(compose_dict, f, sort_keys=False, default_flow_style=False)
    print("=> docker-compose-escala.yml gerado com sucesso!")

if __name__ == "__main__":
    gerar_yaml_escala()
