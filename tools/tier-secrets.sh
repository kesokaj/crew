# tier_secrets DIR: the made-up secrets of a throwaway tier stack (tools/hostingtier, tools/talktier),
# sourced by its stack.sh. The database's container reads db_* as another user; the keys and the token
# only the caller's uid, which runs the server while it holds the machine's Docker socket. What a run
# that was cut off left, test.env and each secret, is removed first, so every file is made anew under
# umask 077, never written into one another mode kept (security reviews of 5ddd59a and 1e00ae4).
tier_secrets() {
    local dir=$1 s
    mkdir -p "$dir" && chmod 0755 "$dir"
    rm -f "$dir/test.env" "$dir"/db_* "$dir/ingress_token" "$dir/jwt_key" "$dir/secret_key"
    for s in db_postgres db_crew db_crew_migrate db_crew_read ingress_token jwt_key; do
        (umask 077 && head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$dir/$s")
    done
    (umask 077 && head -c 32 /dev/urandom > "$dir/secret_key")
    chmod 0600 "$dir/ingress_token" "$dir/jwt_key" "$dir/secret_key"
    chmod 0644 "$dir"/db_*
}
