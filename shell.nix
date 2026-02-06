{ pkgs ? import <nixpkgs> {} }:
let
  goPkg = if pkgs ? go_1_22 then pkgs.go_1_22 else pkgs.go;
  nodePkg = if pkgs ? nodejs_20 then pkgs.nodejs_20 else pkgs.nodejs;
in
pkgs.mkShell {
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
    echo "klitka dev shell: qemu + virtiofsd + go + node + zig ready"
    echo "Note: install connect proto plugins if needed:"
    echo "  go install connectrpc.com/connect/cmd/protoc-gen-connect-go@latest"
    echo "  npm i -D @bufbuild/protoc-gen-es @connectrpc/protoc-gen-connect-es"
  '';
}
