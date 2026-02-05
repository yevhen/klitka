{
  description = "klitkavm development environment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);
    in
    {
      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
          goPkg = if pkgs ? go_1_22 then pkgs.go_1_22 else pkgs.go;
          nodePkg = if pkgs ? nodejs_20 then pkgs.nodejs_20 else pkgs.nodejs;
        in
        {
          default = pkgs.mkShell {
            packages = [
              goPkg
              nodePkg
              pkgs.qemu
              pkgs.zig
              pkgs.protobuf
              pkgs.protoc-gen-go
              pkgs.cpio
              pkgs.gzip
              pkgs.curl
              pkgs.python3
              pkgs.gnumake
              pkgs.git
            ] ++ pkgs.lib.optionals pkgs.stdenv.isLinux [ pkgs.virtiofsd ];
            shellHook = ''
              echo "klitkavm dev shell: qemu + virtiofsd + go + node + zig ready"
              echo "Note: install connect proto plugins if needed:"
              echo "  go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest"
              echo "  npm i -D @bufbuild/protoc-gen-es @connectrpc/protoc-gen-connect-es"
            '';
          };
        });
    };
}
