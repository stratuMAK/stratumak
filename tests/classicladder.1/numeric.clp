_FILES_CLASSICLADDER
_FILE-general.txt
PERIODIC_REFRESH=1
SIZE_NBR_RUNGS=10
SIZE_NBR_BITS=20
SIZE_NBR_WORDS=10
SIZE_NBR_TIMERS=5
SIZE_NBR_MONOSTABLES=5
SIZE_NBR_COUNTERS=5
SIZE_NBR_TIMERS_IEC=5
SIZE_NBR_PHYS_INPUTS=10
SIZE_NBR_PHYS_OUTPUTS=10
SIZE_NBR_ARITHM_EXPR=10
SIZE_NBR_SECTIONS=4
SIZE_NBR_SYMBOLS=10
_/FILE-general.txt
_FILE-sections.csv
#VER=1.0
#NAME000=Numeric
000,0,-1,0,0,0
_/FILE-sections.csv
_FILE-arithmetic_expressions.csv
#VER=2.0
@280/0@:=@270/0@*2
@270/0@>5
@310/0@:=@300/0@+1
@280/1@:=@200/0@
@200/0@:=@200/0@+@270/1@
_/FILE-arithmetic_expressions.csv
_FILE-rung_0.csv
#VER=2.0
#LABEL=Numeric
#COMMENT=s32 and float pin exercise
#PREVRUNG=0
#NEXTRUNG=0
99-0-0/0 , 99-0-0/0 , 60-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
99-0-0/0 , 99-0-0/0 , 20-0-0/1 , 50-0-60/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
99-0-0/0 , 99-0-0/0 , 60-0-0/2 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
1-0-50/0 , 99-0-0/0 , 99-0-0/0 , 60-0-0/4 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
99-0-0/0 , 99-0-0/0 , 60-0-0/3 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0 , 0-0-0/0
_/FILE-rung_0.csv
_FILE-timers.csv
1,0
1,0
1,0
1,0
1,0
_/FILE-timers.csv
_FILE-monostables.csv
1,0
1,0
1,0
1,0
1,0
_/FILE-monostables.csv
_FILE-counters.csv
0
0
0
0
0
_/FILE-counters.csv
_FILE-timers_iec.csv
#VER=1.0
1,0,0
1,0,0
1,0,0
1,0,0
1,0,0
_/FILE-timers_iec.csv
_FILE-symbols.csv
#VER=1.0
%IW0,in-word,s32 input
%QW0,out-word,doubled s32 input
%IF0,in-float,float input
%QF0,out-float,float input plus one
%W0,accum,running total
_/FILE-symbols.csv
