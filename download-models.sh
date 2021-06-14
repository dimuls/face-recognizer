#!/bin/sh

mkdir -p models

wget https://github.com/davisking/dlib-models/raw/master/dlib_face_recognition_resnet_model_v1.dat.bz2 -O ./models
wget https://github.com/davisking/dlib-models/raw/master/shape_predictor_68_face_landmarks.dat.bz2 -O ./models
wget https://github.com/davisking/dlib-models/raw/master/mmod_human_face_detector.dat.bz2 -O ./models

cd models

bunzip dlib_face_recognition_resnet_model_v1.dat.bz2
bunzip shape_predictor_68_face_landmarks.dat.bz2
bunzip mmod_human_face_detector.dat.bz2