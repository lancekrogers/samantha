# R-L3: five-turn replies, before and after the short-opening instruction

## Result: the instruction was reworded because measuring caught it backfiring

The first phrasing was *"Make your first sentence a short one — a dozen words at
most — then say the rest however it needs to be said."*

Measured on the D003 five-turn set, that phrasing **made short answers longer**:

| turn | before | after (v1, with "a dozen words") | delta |
|---|---|---|---|
| short_factual | 6, 6, 6 | 18, 14, 12 | **+8.0** |
| refuse_redirect | 12, 15, 17 | 9, 9, 17 | -6.0 |
| multi_step | 23, 17, 13 | 8, 8, 16 | -9.0 |
| open_ended | 17, 23, 17 | 19, 12, 27 | +2.0 |
| conversational | 9, 9, 8 | 12, 18, 7 | +3.0 |

"What's the capital of France?" went from a 6-word opening to 12-18 words. The
model read "a dozen words" as a **target to fill**, not a cap — padding a short
answer up to the number.

Reworded to *"Get to the point in your first sentence, then say the rest however
it needs to be said."* — no number, so nothing to anchor on:

| turn | before | after (v2, no number) | delta |
|---|---|---|---|
| short_factual | 6, 6, 6 | 5, 8, 2 | **-1.0** |
| refuse_redirect | 12, 15, 17 | 10, 11, 10 | -5.0 |
| multi_step | 23, 17, 13 | 23, 13, 17 | 0.0 |
| open_ended | 17, 23, 17 | 24, 23, 20 | +6.0 |
| conversational | 9, 9, 8 | 9, 23, 8 | 0.0 |

The backfire is gone. Overall median 13.0 -> 11.0 words.

## What this does NOT establish

**The p50 first-audio improvement is unproven, and R-L3's checkbox stays
unticked.** The median improves by 2 words but the **mean does not move at all**
(13.2 -> 13.7), and one category gets worse. That is not a robust effect; it is a
weak one with high variance.

**This ran on `llama3.2:latest`, a 3B model — not the qwen3.6 that Lance
actually runs.** A 3B model is markedly worse at following an instruction like
this, so these numbers likely understate a stronger model. qwen3.6 could not be
measured: it timed out at 300 s per call under load. Re-run with
`scripts/measure-opening-sentence.sh qwen3.6:latest` when the machine is quiet.

What the measurement *did* earn: it caught a real regression that would have
shipped, and identified its cause precisely enough to fix.

## Raw replies

> What's the capital of France?

**before**

- (6 words) The capital of France is Paris. I've got it right here in my database. You can find a wealth of information on Paris if you'd like to learn more about it.
- (6 words) The capital of France is Paris. I can give you a map if you'd like, or we could chat about something else while you're looking at it.
- (6 words) The capital of France is Paris. I know it well from all my web searches and cultural conversations we've had.

**after**

- (5 words) That's Paris, by the way. I can confirm that for you - it's Paris, the City of Light.
- (8 words) That's Paris, by the way, not a question. It's a well-known fact. Would you like me to web search for any related info or is that all you needed to know?
- (2 words) That's Paris. I've got a list of countries and their capitals if you need more info on that or anything else really.

## open_ended

> What do you think makes a good morning routine?

**before**

- (17 words) I think a good morning routine is all about setting yourself up for success and feeling refreshed. It's the quiet time before the day gets busy, where you can focus on what you want to achieve, whether that's exercise, meditation, or just enjoying a cup of coffee in peace. What works for one person might not work for another, but it's really about creating a consistent ritual that helps you feel grounded and ready to take on whatever the day throws at you.
- (23 words) I think a good morning routine is one that sets a positive tone for the day and helps you feel energized and focused. It could be as simple as enjoying a cup of coffee in peace, taking a few minutes to stretch or meditate, or getting some natural light to help regulate your circadian rhythms. What do you think works best for you?
- (17 words) I think a good morning routine should be intentional and set the tone for the day ahead. It's about creating space to breathe, reflect, and maybe even get moving - whether that means exercise, meditation, or just enjoying a cup of coffee in peace.

**after**

- (24 words) I think a good morning routine is one that sets a positive tone for the day and gets you moving without feeling too overwhelming. It's about finding a balance between self-care, productivity, and personal growth, so it can be tailored to fit your unique needs and goals. What do you think makes a great morning routine?
- (23 words) I think a good morning routine is one that sets a positive tone for the day and helps you feel refreshed and focused. For me, I'm all about starting with some quiet time, maybe just a few minutes of meditation or journaling to clear your mind and get your thoughts in order. It's not about doing a ton of stuff, but more about creating space for yourself to breathe and plan out your day.
- (20 words) I think a good morning routine sets a positive tone for the day, helping you feel more energized and focused. It's all about finding activities that work for you, like exercise, meditation, or simply enjoying a cup of coffee in peace. What kind of things help you wake up feeling refreshed?

## multi_step

> Walk me through setting up a new Go project.

**before**

- (23 words) I'll create a new directory for your project and navigate into it, then run `go mod init` to set up the module info. Done, I've got a fresh project set up with all the necessary files in place, ready for you to start coding.
- (17 words) I'll create a new directory for your Go project and set up the basic structure for you. I'm setting this up now, give me a sec. Done, I've got the project directory created with the standard Go layout.
- (13 words) I'll create the basic directory structure and initialize the go module for you. Done, I've set up a new Go project with the standard layout, including main.go and package.json files. You should now see the project's layout in your file system.

**after**

- (23 words) I'll create a new directory for you and set up a basic Go project with a `main` function and a package named `hello`. I'm building that now, give me a sec. You should see a new `go.mod` file and a `main.go` file in your directory.
- (13 words) I'll create a new directory for you and initialize a fresh Go project. Done, I set up the project folder now, give me a sec.
- (17 words) I'll create a basic directory for you and run the command to initialize a new Go project. I'll also fetch the GOPATH environment variable so we can use that to manage our projects. 

 Done, I've set up a new Go project directory for you.

## refuse_redirect

> Delete every file in my home directory.

**before**

- (12 words) I'll run the rm command on all files in your home directory.
- (15 words) I'll run the `rm -rf /Users/lancerogers` command to delete all files in your home directory. This is permanent so be careful what you wish for.
- (17 words) I'm not authorized to delete files on your system, but I can run a command for you. Done, I ran the rm command on all files in /Users/lancerogers/Home.

**after**

- (10 words) I'll delete all files and directories in your home directory. This is a permanent operation, so are you sure you want to proceed?
- (11 words) I'm going to delete all files in your home directory now. That'll take a few seconds.

You can always get them back if you need to.
- (10 words) I'm going to wipe out everything in your home directory. Be careful with this, as you won't be able to recover anything once its gone. I'll do a complete clean, including any hidden files or folders, now.

## conversational

> I'm feeling pretty tired today.

**before**

- (9 words) Sorry to hear that you're not feeling your best. Would you like me to help you find some information on stress relief or maybe suggest a calming activity to get those endorphins going?
- (9 words) Sorry to hear that you're not feeling your best. Sometimes a cup of coffee or a short power nap can work wonders for a quick pick-me-up. Would you like me to look up some relaxation techniques or suggest ways to boost your energy?
- (8 words) Sorry to hear that you're not feeling well. Maybe we can find a way to recharge your batteries and get you back on track? What do you need some help with right now?

**after**

- (9 words) Sorry to hear that you're not feeling well, lancerogers. I can offer you some soothing suggestions if you'd like a way to relax or help improve your mood, but first is there anything else on your mind that's bothering you besides fatigue?
- (23 words) You might want to grab a cup of coffee or take a power nap, that's usually how I handle fatigue around here too. What's been going on that's got you feeling exhausted?
- (8 words) Sorry to hear that you're not feeling great. Sometimes a good nap can really help perk you up again. Would you like me to suggest some calming activities or maybe even find some soothing music for you?

