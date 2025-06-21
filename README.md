# CrowdLlama

<img src="image.png" alt="CrowdLlama" width="50%">

CrowdLlama is a distributed system for discovering and coordinating GPU worker nodes using a P2P network. It leverages libp2p and IPFS technologies to enable decentralized resource discovery and metadata sharing among nodes.

## Features
- DHT-based peer discovery
- Worker nodes advertise their GPU capabilities and supported models
- Simple metadata protocol for querying worker information

## Components
- **DHT Server**: Runs a custom DHT node to facilitate peer discovery.
- **Worker**: Registers itself on the DHT and advertises its GPU resources and supported models.
- **Consumer**: (Planned) Will discover and utilize available workers for distributed tasks.

## Getting Started
1. Clone the repository:
   ```sh
   git clone https://github.com/matiasinsaurralde/crowdllama.git
   cd crowdllama
   ```
2. Build the components:
   ```sh
   go build ./cmd/dht
   go build ./cmd/worker
   ```
3. Run the DHT server:
   ```sh
   ./dht
   ```
4. Start a worker node:
   ```sh
   ./worker start
   ```

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details. 