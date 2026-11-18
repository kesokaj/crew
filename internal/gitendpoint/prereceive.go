package gitendpoint

// preReceive refuses an update of a ref the request may not push, the removal of any ref, a main that
// does not go on from the main there was, a ref set to no commit, a push of more commits than
// CREW_NEW_COMMITS, a commit of more bytes than CREW_COMMIT_BYTES, and a tree past the bound, of its paths, its files' bytes, a path or its listing
// (the CREW_ variables of treeBoundEnv), or one not checked within CREW_TREE_SECONDS. CREW_PUSH_REFS
// is set by the endpoint for each
// request: a ref, or a prefix ending in / for the refs under it. A zero id is a removal, of either
// hash. When the request has path rules, CREW_PUSH_PATHS, the server's own program tests the files
// the push changes (13.8), within CREW_TREE_SECONDS too (since 1.07); a request with rules and no
// program is refused.
const preReceive = `#!/bin/sh
# The git endpoint's own hook (design 6.1), outside every repository: a run pushes only its refs, and
# changes only the files its ticket may change.
if [ "${1:-}" = crew-trees ]; then
	# The trees of the push, each listed once: every ref's tip's, and those of the commits it brings, of
	# which there are at most CREW_NEW_COMMITS. Each listing counts its trees, is cut at
	# CREW_LIST_BYTES, and stops at the first path past the bound. A ref set to no commit, and a commit,
	# a tree or a listing git cannot give, are refused.
	trees=$(while read old new ref; do
		[ -z "$ref" ] && continue
		# A branch's tip is a commit, never a tag of one, whose objects no check below reads (since 1.09).
		if ! git rev-parse -q --verify "$new^{commit}" >/dev/null || [ "$(git cat-file -t "$new" 2>/dev/null)" != commit ]; then
			echo "crew: $ref is not a commit Crew can read" >&2
			echo crew-refused
			continue
		fi
		if ! commits=$(git rev-list --max-count="$((CREW_NEW_COMMITS + 1))" "$new" --not --all); then
			echo "crew: the commits of $ref cannot be read" >&2
			echo crew-refused
			continue
		fi
		if [ "$(printf '%s\n' "$commits" | grep -c .)" -gt "$CREW_NEW_COMMITS" ]; then
			echo "crew: the push of $ref brings more than $CREW_NEW_COMMITS commits" >&2
			echo crew-refused
			continue
		fi
		# The tip and each new commit, its headers and its message, is at most CREW_COMMIT_BYTES, as git
		# gives its size without reading it, before the server's program or a history reads them; git's
		# own rev-parse and rev-list above have read them, in git's memory. Only an awk that says ok
		# takes the push: a size git cannot give, one fewer than asked, or an awk that fails refuses it
		# (since 1.09).
		n=$(printf '%s\n' "$new" $commits | grep -c .)
		over=$({ printf '%s\n' "$new" $commits | git cat-file --batch-check='%(objectname) %(objectsize)' || echo crew-check-failed; } |
			awk -v max="$CREW_COMMIT_BYTES" -v n="$n" '
			{ if ($0 == "crew-check-failed" || NF != 2 || $2 !~ /^[0-9]+$/ || max !~ /^[0-9]+$/) { r = "unread"; exit }
			  if ($2 + 0 > max + 0) { r = $1 " " $2; exit }
			  c++ }
			END { if (r == "" && c != n) r = "unread"; print (r == "" ? "ok" : r) }')
		case "$over" in
		ok) ;;
		"" | unread)
			echo "crew: the commits of $ref cannot be read" >&2
			echo crew-refused
			continue ;;
		*)
			echo "crew: the commit ${over%% *} of $ref is ${over#* } bytes, more than $CREW_COMMIT_BYTES that Crew takes" >&2
			echo crew-refused
			continue ;;
		esac
		# Each of them is in UTF-8, with no encoding header or one that says so: git turns another
		# encoding into UTF-8 as it prints a history, up to three times as long as the hook measured.
		# Only an awk that says ok takes the push (since 1.09).
		bad=$({ git log --no-walk=unsorted --format='%H %e' "$new" $commits || echo crew-check-failed; } |
			awk '
			{ if ($0 == "crew-check-failed") { r = "unread"; exit }
			  e = $0; sub(/^[^ ]*/, "", e); sub(/^ /, "", e); e = tolower(e)
			  if (e != "" && e != "utf-8" && e != "utf8") { r = $1; exit } }
			END { print (r == "" ? "ok" : r) }')
		case "$bad" in
		ok) ;;
		"" | unread)
			echo "crew: the commits of $ref cannot be read" >&2
			echo crew-refused
			continue ;;
		*)
			echo "crew: the commit $bad of $ref is not in UTF-8" >&2
			echo crew-refused
			continue ;;
		esac
		git log --no-walk=unsorted --format=%T "$new" $commits || echo crew-refused
	done | sort -u)
	case "$trees" in *crew-refused*) exit 1 ;; esac
	for t in $trees; do
		{ git ls-tree -r -t -l --full-tree "$t" || printf '\ncrew-listing-failed\n'; } | head -c "$((CREW_LIST_BYTES + 1))" |
			awk -v n="$CREW_TREE_PATHS" -v b="$CREW_TREE_BYTES" -v pl="$CREW_PATH_BYTES" -v lb="$CREW_LIST_BYTES" '
			{ c++; l += length($0) + 1; if ($4 ~ /^[0-9]+$/) s += $4; p = $0; sub(/^[^\t]*\t/, "", p)
			  if ($0 == "crew-listing-failed" || c > n + 0 || s > b + 0 || length(p) > pl + 0 || l > lb + 0) { over = 1; exit } }
			END { exit over }' || {
			echo "crew: a tree of this push holds more than $CREW_TREE_PATHS paths, $CREW_TREE_BYTES bytes of files or a path of $CREW_PATH_BYTES bytes, more than Crew checks" >&2
			exit 1
		}
	done
	exit 0
fi
input=$(cat)
printf '%s\n' "$input" | while read old new ref; do
	[ -z "$ref" ] && continue
	ok=
	for allowed in $CREW_PUSH_REFS; do
		case "$allowed" in
		*/) case "$ref" in "$allowed"?*) ok=1 ;; esac ;;
		*) [ "$ref" = "$allowed" ] && ok=1 ;;
		esac
	done
	if [ -z "$ok" ]; then
		echo "crew: this run may not push $ref" >&2
		exit 1
	fi
	case "$new" in
	*[!0]*) ;;
	*) echo "crew: $ref may not be removed" >&2; exit 1 ;;
	esac
	case "$old" in
	*[!0]*) if [ "$ref" = refs/heads/main ] && ! git merge-base --is-ancestor "$old" "$new"; then
		echo "crew: main only goes on from where it is: fetch it, and push what follows it" >&2
		exit 1
	fi ;;
	esac
done || exit 1
# A tree past the bound is refused before anything lists it whole, the path rules below included; the
# check runs as this hook again, under a time limit (6.1, since 1.03).
printf '%s\n' "$input" | timeout "$CREW_TREE_SECONDS" "$0" crew-trees
case $? in
0) ;;
124) echo "crew: the trees of this push could not be checked in $CREW_TREE_SECONDS seconds" >&2; exit 1 ;;
*) exit 1 ;;
esac
if [ -n "$CREW_PUSH_PATHS" ]; then
	if [ -z "$CREW_HOOK" ]; then
		echo "crew: the files of this push cannot be tested" >&2
		exit 1
	fi
	# Under the same time limit, and killed 5 seconds after it: the server's program takes the signal
	# to stop as a request, and its own work watches no time (6.1, since 1.07). A kill by the kernel, as
	# for its memory, ends the same way, so the text names both.
	printf '%s\n' "$input" | timeout -k 5 "$CREW_TREE_SECONDS" "$CREW_HOOK" git-pre-receive
	case $? in
	0) ;;
	124 | 137) echo "crew: the test of the files of this push did not end within $CREW_TREE_SECONDS seconds, or was stopped" >&2; exit 1 ;;
	*) exit 1 ;;
	esac
fi
`
