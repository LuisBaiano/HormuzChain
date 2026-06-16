import random
import yaml

def gerar_yaml_completo():
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

    # 4. 8 Drones (2 por setor/broker)
    drone_sectors = [
        {"setor": "NW", "x": 250, "y": 750, "port": 6000},
        {"setor": "NE", "x": 750, "y": 750, "port": 6001},
        {"setor": "SW", "x": 250, "y": 250, "port": 6002},
        {"setor": "SE", "x": 750, "y": 250, "port": 6003}
    ]
    all_broker_ports = [6000, 6001, 6002, 6003]
    drone_idx = 1
    for s_info in drone_sectors:
        primary = s_info["port"]
        ordered = [primary] + [p for p in all_broker_ports if p != primary]
        brokers_str = ",".join(f"127.0.0.1:{p}" for p in ordered)
        for j in range(2):
            id_drone = f"Drone_{s_info['setor']}_{drone_idx}"
            services[f"drone{drone_idx}"] = {
                "build": {"context": "./code", "dockerfile": "Dockerfile.drone"},
                "container_name": f"hormuznet_{id_drone.lower()}",
                "network_mode": "host",
                "command": [f"-id={id_drone}", f"-brokers={brokers_str}", f"-x={s_info['x']}", f"-y={s_info['y']}"],
                "restart": "on-failure"
            }
            drone_idx += 1

    # 5. 8 Sensores (2 por setor/broker)
    types = ["radar", "sonar", "boia", "visual", "meteo"]
    sensor_idx = 1
    for b in brokers_base:
        setor_short = b["setor"].split("_")[1].lower()
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
            id_sensor = f"{t}_{setor_short}_{sensor_idx}"
            services[f"sensor_{sensor_idx}"] = {
                "build": {"context": "./code", "dockerfile": "Dockerfile.sensor"},
                "container_name": f"hormuznet_{id_sensor}",
                "network_mode": "host",
                "command": [f"-id={id_sensor}", f"-tipo={t}", f"-setor={b['setor']}", "-broker=224.1.2.3:9876", "-intervalo=20000", f"-x={x}", f"-y={y}"],
                "restart": "on-failure"
            }
            sensor_idx += 1

    # 6. Navios Iniciais (4 vessels pertencentes às empresas padrão)
    initial_vessels = [
        {
            "id": "vessel_maersk_01",
            "company_name": "Maersk",
            "company_addr": "0x280f33adb69caa3e5c8c",
            "company_priv": "9bfb94f11c9b617fb14a2452f0e243759147ef9eb8bf60d7121787d4504eafa4",
            "x": 150, "y": 750, "broker_api": "http://localhost:7000"
        },
        {
            "id": "vessel_msc_01",
            "company_name": "MSC",
            "company_addr": "0x2a9621c924cf329f550a",
            "company_priv": "cb93bbfc68a40806d3b48a97e60faa0df6959b127e677bb175a55267a73c5e20",
            "x": 650, "y": 750, "broker_api": "http://localhost:7001"
        },
        {
            "id": "vessel_cma_01",
            "company_name": "CMA_CGM",
            "company_addr": "0x7daccdb0e3eb3ce3d768",
            "company_priv": "682ea73bd47e2d0c34856edeb0b10de26f00e47ae654ff04d1a4e580fbcaede7",
            "x": 150, "y": 250, "broker_api": "http://localhost:7002"
        },
        {
            "id": "vessel_hapag_01",
            "company_name": "Hapag_Lloyd",
            "company_addr": "0xf7d808577df8b4454e18",
            "company_priv": "303b1fa143cff773c69665a3891971bde28806c59ea2d4930f9fb8ac2c861a0a",
            "x": 650, "y": 250, "broker_api": "http://localhost:7003"
        }
    ]

    all_api_ports = [7000, 7001, 7002, 7003]
    for idx, v in enumerate(initial_vessels):
        primary_port = int(v["broker_api"].split(":")[-1])
        ordered_api_ports = [primary_port] + [p for p in all_api_ports if p != primary_port]
        api_fallback_str = ",".join(f"http://localhost:{p}" for p in ordered_api_ports)

        services[f"vessel_{idx+1}"] = {
            "build": {"context": "./code", "dockerfile": "Dockerfile.vessel"},
            "container_name": f"hormuzchain_{v['id']}",
            "network_mode": "host",
            "environment": {
                "VESSEL_ID": v["id"],
                "COMPANY_NAME": v["company_name"],
                "COMPANY_ADDR": v["company_addr"],
                "COMPANY_PRIV_KEY": v["company_priv"],
                "BROKER_API": api_fallback_str,
                "X": str(v["x"]),
                "Y": str(v["y"])
            },
            "restart": "on-failure"
        }

    compose_dict = {
        "version": "3.8",
        "services": services
    }

    with open("docker-compose-all.yml", "w") as f:
        yaml.dump(compose_dict, f, sort_keys=False, default_flow_style=False)

if __name__ == "__main__":
    gerar_yaml_completo()
